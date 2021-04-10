package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type lxWebsocket struct {
	address string

	running bool

	ws               *websocket.Conn
	ctlChannel       chan loxoneControlMessage
	binChannel       chan []byte
	stsChannel       chan loxoneStatusMessage
	reconnectChannel chan bool
	stopKeepAlive    chan bool

	sendLock sync.Mutex
}

type loxoneControlMessage struct {
	LL struct {
		Code    string
		Control string
		Value   interface{}
	}
}

type loxoneStatusMessage struct {
	MsgType byte
	Data    []byte
}

func newLxWebsocket(addr string) (lws lxWebsocket, err error) {
	conn, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf("ws://%s/ws/rfc6455", addr), nil)
	if err != nil {
		return
	}
	lws.ws = conn
	lws.address = addr
	lws.ctlChannel = make(chan loxoneControlMessage, 10)
	lws.binChannel = make(chan []byte, 2)
	lws.stsChannel = make(chan loxoneStatusMessage, 10)
	lws.stopKeepAlive = make(chan bool, 1)
	lws.reconnectChannel = make(chan bool, 2)

	go lws.autoReceiver()

	return
}

func (lws *lxWebsocket) getControlMessage() (lcm loxoneControlMessage, err error) {
	select {
	case lcm = <-lws.ctlChannel:
		break
	case <-time.After(10 * time.Second):
		err = fmt.Errorf("No message for 10 seconds")
	}
	return
}

func (lws *lxWebsocket) getBinaryMessage() (data []byte, err error) {
	select {
	case data = <-lws.binChannel:
		break
	case <-time.After(10 * time.Second):
		err = fmt.Errorf("No message for 10 seconds")
	}
	return
}

func (lws *lxWebsocket) sendTextMessage(cmd []byte) (err error) {
	lws.sendLock.Lock()
	err = lws.ws.WriteMessage(websocket.TextMessage, []byte(cmd))
	lws.sendLock.Unlock()
	if err != nil {
		log.Printf("Error sending TextMessage to websocket: %s", err)
	}
	return
}

func (lws *lxWebsocket) sendRecvControl(cmd string) (msg loxoneControlMessage, err error) {
	log.Printf("TX: %s", cmd)
	err = lws.sendTextMessage(([]byte(cmd)))
	if err != nil {
		return
	}
	return lws.getControlMessage()
}

func (lws *lxWebsocket) sendRecvBinary(cmd string) (data []byte, err error) {
	log.Printf("TX: %s", cmd)
	err = lws.sendTextMessage(([]byte(cmd)))
	if err != nil {
		return
	}
	return lws.getBinaryMessage()
}

func (lws *lxWebsocket) recvMessage() (err error) {
	// Read header
	_, msg, err := lws.ws.ReadMessage()
	if err != nil {
		log.Printf("ReadMessage() failed: %s", err)
		return
	}
	if msg[0] != 0x03 {
		err = fmt.Errorf("Invalid header received, 0x%02X vs 0x03 expected", msg[0])
		return
	}
	// If it's an estimated header, get the accurate size
	if msg[2] == 0x80 {
		return lws.recvMessage()
	}

	msgType := msg[1]
	msgLen := binary.LittleEndian.Uint32(msg[4:])
	switch msgType {
	case 5:
		log.Print("OOS detected, waiting 1 minute then trying to reconnect...")
		time.Sleep(time.Minute)
		return
	case 6:
		// keepalive response...
		return
	}

	log.Printf("RX: %02X  ->  Type %d, length %d", msg, msgType, msgLen)

	_, msg, err = lws.ws.ReadMessage()
	if err != nil {
		err = fmt.Errorf("Unable to read from websocket: %s", err)
		return
	}
	if len(msg) == 0 {
		err = fmt.Errorf("Zero byte packet received?")
		return
	}
	//log.Printf("  : %s", string(msg))
	if msg[0] == 0x03 {
		err = fmt.Errorf("Header received when message was expected??? %s", string(msg))
		return
	}

	switch msgType {
	case 0:
		var respData loxoneControlMessage
		log.Printf("%s", msg)
		if err = json.Unmarshal(msg, &respData); err != nil {
			return
		}
		lws.ctlChannel <- respData
		return
	case 1:
		lws.binChannel <- msg
		return
	case 2, 3, 4, 7:
		lws.stsChannel <- loxoneStatusMessage{msgType, msg}
		//	ValueState = 2
		//  TextState = 3
		//  DaytimerState = 4
		//  OOS = 5
		//  Keepalive = 6
		//  WeatherState = 7
	}
	return
}

func (lws *lxWebsocket) autoReceiver() {
	log.Print("Starting autoReceiver...")
	lws.running = true
	for {
		err := lws.recvMessage()
		if err != nil {
			log.Printf("Error receiving a message: %s", err)
			break
		}
	}
	log.Print("autoReceiver stopped")
	lws.running = false
	lws.reconnectChannel <- true
	lws.stopKeepAlive <- true
}

func (lws *lxWebsocket) StartKeepAlive() {
	log.Print("Starting keepalive sending...")
	go func() {
		msg := []byte("keepalive")
	keepAliveLoop:
		for {
			err := lws.sendTextMessage(msg)
			if err != nil {
				log.Print("Error sending keepalive to server")
				break
			}
			select {
			case <-time.After(5 * time.Second):
				continue
			case <-lws.stopKeepAlive:
				log.Print("Exiting keepalive as reconnect signalled...")
				break keepAliveLoop
			}
		}
		log.Print("Stopped keepalive sending...")
	}()
}
