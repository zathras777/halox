package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"hash"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type loxoneServer struct {
	address  string
	userName string
	passWord string

	apiKey    string
	publicKey *rsa.PublicKey
	ws        *lxWebsocket

	aesKey        []byte
	aesIV         []byte
	encSessionKey string

	salt    []byte
	srvSalt []byte

	token           string
	tokenExpiration time.Time

	updatesEnabled bool
	updateChannel  chan loxoneStatusMessage
}

var loxoneTimeBase time.Time = time.Date(2009, 01, 01, 0, 0, 0, 0, time.UTC)

func PKCS5Padding(ciphertext []byte, blockSize int) []byte {
	padding := blockSize - len(ciphertext)%blockSize
	padtext := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(ciphertext, padtext...)
}

func newLoxoneServer(host string, port int, user, pw string) *loxoneServer {
	return &loxoneServer{address: fmt.Sprintf("%s:%d", host, port),
		userName: user,
		passWord: pw}
}

/* Can be used for reconnections... */
func (ls *loxoneServer) connect() error {
	if len(ls.apiKey) == 0 {
		if err := ls.getApiKey(); err != nil {
			return fmt.Errorf("Unable to get server information from loxone @ %s: %s", ls.address, err)
		}
	}
	if ls.publicKey == nil {
		if err := ls.getPublicKey(); err != nil {
			return fmt.Errorf("Unable to get public key from Loxone @ %s: %s", ls.address, err)
		}
	}
	if ls.ws != nil {
		ls.ws.ws.Close()
		ls.ws = nil
	}
	if err := ls.openWebsocket(); err != nil {
		return fmt.Errorf("Unable to open a websocket: %s", err)
	}
	if err := ls.doKeyExchange(); err != nil {
		return fmt.Errorf("Unable to complete a key exchange: %s", err)
	}
	if err := ls.getToken(); err != nil {
		return err
	}
	go ls.serverMonitor()
	if ls.updatesEnabled {
		ls.enableUpdates()
	}
	log.Printf("Connected & authenticated with Loxone server @ %s", ls.address)
	return nil
}

func (ls loxoneServer) makeURL(uri string) string {
	return fmt.Sprintf("http://%s/%s", ls.address, uri)
}

func (ls *loxoneServer) getApiKey() error {
	value, err := getLoxoneUrl(ls.makeURL("jdev/cfg/apiKey"))
	if err != nil {
		return err
	}
	var apiData struct {
		SNR     string
		Version string
		Key     string
	}
	err = json.Unmarshal([]byte(strings.ReplaceAll(value, "'", "\"")), &apiData)
	if err != nil {
		return err
	}
	ls.apiKey = apiData.Key
	log.Printf("Found Loxone Server. Serial %s, Version %s", apiData.SNR, apiData.Version)
	return nil
}

func (ls *loxoneServer) getPublicKey() error {
	value, err := getLoxoneUrl(ls.makeURL("jdev/sys/getPublicKey"))
	if err != nil {
		return err
	}
	pKey := strings.ReplaceAll(value, "CERTIFICATE", "PUBLIC KEY")
	pKey = strings.ReplaceAll(pKey, "KEY-----", "KEY-----\n")
	pKey = strings.ReplaceAll(pKey, "-----END", "\n-----END")

	block, _ := pem.Decode([]byte(pKey))
	if block == nil {
		return fmt.Errorf("Unable to decode the PEM string...")
	}

	parsedKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return err
	}
	ls.publicKey = parsedKey.(*rsa.PublicKey)
	return nil
}

func (ls *loxoneServer) openWebsocket() error {
	ws, err := newLxWebsocket(ls.address)
	if err != nil {
		return err
	}
	ls.ws = &ws
	return nil
}

func (ls *loxoneServer) doKeyExchange() error {
	aesKey, err := ls.getSessionKey()

	ctlData, err := ls.ws.sendRecvControl("jdev/sys/keyexchange/" + ls.encSessionKey)
	if err != nil {
		return err
	}

	b64, err := base64.StdEncoding.DecodeString(ctlData.LL.Value.(string))
	b64 = PKCS5Padding(b64, 16)
	ls.srvSalt = make([]byte, 16)
	aesKey.Decrypt(ls.srvSalt, b64)
	return nil
}

func (ls *loxoneServer) getSessionKey() (cipher.Block, error) {
	update_rqd := false
	if len(ls.aesKey) == 0 {
		ls.aesKey = make([]byte, 32)
		rand.Read(ls.aesKey)
		update_rqd = true
		log.Printf("AES Key: %02x", ls.aesKey)
	}
	if len(ls.aesIV) == 0 {
		ls.aesIV = make([]byte, 16)
		rand.Read(ls.aesIV)
		update_rqd = true
		log.Printf("AES IV : %02x", ls.aesIV)
	}
	if update_rqd {
		payload := fmt.Sprintf("%02x:%02x", ls.aesKey, ls.aesIV)
		encryptedBytes, err := rsa.EncryptPKCS1v15(rand.Reader, ls.publicKey, []byte(payload))
		if err != nil {
			panic(err)
		}
		ls.encSessionKey = base64.StdEncoding.EncodeToString(encryptedBytes)
	}
	return aes.NewCipher(ls.aesKey)
}

func (ls *loxoneServer) getKey() (empty hash.Hash, keyed hash.Hash, salt string, err error) {
	ctlData, err := ls.ws.sendRecvControl("jdev/sys/getkey2/" + ls.userName)
	if err != nil {
		return
	}

	respData := ctlData.LL.Value.(map[string]interface{})
	key, err := hex.DecodeString(respData["key"].(string))
	salt = respData["salt"].(string)

	switch respData["hashAlg"].(string) {
	case "SHA1":
		empty = sha1.New()
		keyed = hmac.New(sha1.New, key)
	case "SHA256":
		empty = sha256.New()
		keyed = hmac.New(sha256.New, key)
	}
	return

}
func (ls *loxoneServer) getToken() error {
	empty, keyed, salt, err := ls.getKey()

	empty.Write([]byte(ls.passWord + ":" + salt))
	uSum := fmt.Sprintf("%02X", empty.Sum(nil))

	keyed.Write([]byte(ls.userName + ":" + uSum))

	uuid := "098802e1-02b4-603c-ffffeee000d80cfd"
	info := "Test App 112"

	cmd := fmt.Sprintf("jdev/sys/getjwt/%s/%s/2/%s/%s", fmt.Sprintf("%02X", keyed.Sum(nil)), ls.userName, uuid, info)
	ctlData, err := ls.sendEncryptedCommand(cmd)
	if err != nil {
		return nil
	}
	if ctlData.LL.Code != "200" {
		log.Printf("Failed to get token. Code %s, %s", ctlData.LL.Code, ctlData.LL.Value)
		return fmt.Errorf("Unable to get a token")
	}
	ls.updateToken(ctlData)
	return nil
}

func (ls *loxoneServer) updateToken(ctlData loxoneControlMessage) {
	tokenData := ctlData.LL.Value.(map[string]interface{})
	ls.token = tokenData["token"].(string)
	offset := int64(tokenData["validUntil"].(float64))
	ls.tokenExpiration = loxoneTimeBase.Add(time.Duration(offset) * time.Second)
	log.Printf("Token Received: '%s', valid until %s", ls.token, ls.tokenExpiration)
}

func (ls *loxoneServer) getTokenHash() (string, error) {
	_, keyed, _, err := ls.getKey()
	if err != nil {
		return "", err
	}
	keyed.Write([]byte(ls.token))
	return fmt.Sprintf("%02x", keyed.Sum(nil)), nil
}

func (ls *loxoneServer) checkToken() error {
	tokenHash, err := ls.getTokenHash()
	if err != nil {
		return err
	}
	cmd := fmt.Sprintf("jdev/sys/checktoken/%s/%s", tokenHash, ls.userName)
	ctlData, err := ls.sendEncryptedCommand(cmd)
	if err != nil {
		return err
	}
	ls.updateToken(ctlData)
	return nil
}

func (ls *loxoneServer) refreshToken() error {
	tokenHash, err := ls.getTokenHash()
	if err != nil {
		return err
	}
	cmd := fmt.Sprintf("jdev/sys/refreshjwt/%s/%s", tokenHash, ls.userName)
	ctlData, err := ls.sendEncryptedCommand(cmd)
	if err != nil {
		return nil
	}

	code, err := strconv.Atoi(ctlData.LL.Code)
	if err != nil || code != 200 {
		return fmt.Errorf("Error response to refresh token request: %s %s", ctlData.LL.Code, ctlData.LL.Value)
	}
	ls.updateToken(ctlData)
	return nil
}

func (ls *loxoneServer) serverMonitor() {
monitorLoop:
	for {
		remaining := ls.tokenExpiration.Sub(time.Now())
		select {
		case <-ls.ws.reconnectChannel:
			log.Print("serverMonitor: reconnect()")
			if err := ls.connect(); err != nil {
				log.Printf("Exiting server monitor as unable to connect to server: %s", err)
				break monitorLoop
			}
		case <-time.After(remaining):
			log.Printf("serverMonitor: token expired, refreshing...")
			ls.refreshToken()
		}
	}
}

func (ls *loxoneServer) getStructureFile() (rv map[string]interface{}, err error) {
	sData, err := ls.ws.sendRecvBinary("data/LoxApp3.json")
	if err != nil {
		return
	}
	json.Unmarshal(sData, &rv)
	return
}

func (ls *loxoneServer) enableUpdates() (err error) {
	_, err = ls.ws.sendRecvControl("jdev/sps/enablebinstatusupdate")
	if err != nil {
		return
	}
	ls.updateChannel = ls.ws.stsChannel
	ls.updatesEnabled = true
	ls.ws.StartKeepAlive()
	return
}

func (ls loxoneServer) sendCommand(cmd string) error {
	msg, err := ls.ws.sendRecvControl(cmd)
	if msg.LL.Code != "200" {
		log.Printf("Error sending command to Loxone: Code %s, %s", msg.LL.Code, msg.LL.Value)
	} else {
		log.Print("Message sent to Loxone server OK")
	}
	return err
}

func (ls *loxoneServer) sendEncryptedCommand(cmd string) (ctlData loxoneControlMessage, err error) {
	if len(ls.salt) == 0 {
		ls.salt = make([]byte, 2)
		rand.Read(ls.salt)
		log.Print("New salt obtained")
	}
	aesKey, err := ls.getSessionKey()
	if err != nil {
		return
	}
	fullCmd := fmt.Sprintf("salt/%02X/%s\000", ls.salt, cmd)
	//	log.Printf("sendEncryptedCommand: [%d] '%s'\n", len(fullCmd), fullCmd)
	fullCmdBytes := []byte(fullCmd)
	if len(fullCmdBytes)%16 != 0 {
		//		log.Printf("Padding %d byte command", len(fullCmd))
		fullCmdBytes = PKCS5Padding([]byte(fullCmd), 16)
	}

	enc := make([]byte, len(fullCmdBytes))
	cbc := cipher.NewCBCEncrypter(aesKey, ls.aesIV)
	cbc.CryptBlocks(enc, fullCmdBytes)
	//	log.Printf("enc => '%02X'\n", enc)
	b64 := base64.StdEncoding.EncodeToString(enc)
	escaped := url.QueryEscape(b64)

	return ls.ws.sendRecvControl("jdev/sys/enc/" + escaped)
}

func getLoxoneUrl(url string) (value string, err error) {
	resp, err := http.Get(url)
	if err != nil {
		return
	}
	if resp.StatusCode != 200 {
		err = fmt.Errorf("HTTP request returned incorrect status %d expecting 200", resp.StatusCode)
		return
	}
	var jsonData map[string]interface{}
	if err = json.NewDecoder(resp.Body).Decode(&jsonData); err != nil {
		return
	}
	ll, ck := jsonData["LL"]
	if !ck {
		err = fmt.Errorf("Incorrect JSON data returned?")
		return
	}
	var llData map[string]interface{} = ll.(map[string]interface{})
	code, ck := llData["Code"]
	if !ck {
		code, ck = llData["code"]
		if !ck {
			err = fmt.Errorf("JSON data did not contain a return code as expected")
			return
		}
	}
	codeVal, err := strconv.Atoi(code.(string))
	if codeVal != 200 {
		err = fmt.Errorf("Returned code was not the expected 200 but %s", code)
		return
	}
	value = llData["value"].(string)
	return
}
