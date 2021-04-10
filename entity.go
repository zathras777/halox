package main

import (
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"strings"

	"github.com/google/uuid"
)

type loxoneEntity struct {
	UUID       uuid.UUID
	Name       string
	Type       string
	uuidAction uuid.UUID
	states     map[string]uuid.UUID
}

func uuidFromLoxoneString(uuidStr string) (uu uuid.UUID, err error) {
	return uuid.Parse(strings.ReplaceAll(uuidStr, "-", ""))
}

func newLoxoneEntity(uuidStr string, data map[string]interface{}) loxoneEntity {
	uu, err := uuidFromLoxoneString(uuidStr)
	if err != nil {
		log.Printf("Unable to parse UUID '%s': %s", uuidStr, err)
	}
	uua, err := uuidFromLoxoneString(data["uuidAction"].(string))
	if err != nil {
		log.Printf("Unable to parse UUID '%s': %s", uuidStr, err)
	}

	le := loxoneEntity{
		UUID: uu, Name: data["name"].(string), Type: data["type"].(string), uuidAction: uua}
	le.states = make(map[string]uuid.UUID)
	for st, uus := range data["states"].(map[string]interface{}) {
		uut, err := uuidFromLoxoneString(uus.(string))
		if err != nil {
			log.Printf("Invalid UUID string: %s. %s", uus, err)
			continue
		}
		le.states[st] = uut
	}
	return le
}

func (le loxoneEntity) hassYaml() string {
	ss := fmt.Sprintf("  - platform: mqtt\n    name: %s\n    command_topic: \"loxone/%s/action\"\n    unique_id: %s\n", le.Name, le.uuidAction, le.UUID)
	stateuu, ck := le.states["active"]
	if ck {
		ss += fmt.Sprintf("    state_topic: \"loxone/%s/state\"\n", stateuu)
		ss += "    payload_on: \"1.000000\"\n    payload_off: \"0.000000\"\n"
	}
	return ss + "\n"
}

func (le loxoneEntity) actionCommand(val []byte) string {
	var cmdVal string
	switch string(val) {
	case "1.000000":
		cmdVal = "On"
	case "0.000000":
		cmdVal = "Off"
	}
	return fmt.Sprintf("jdev/sps/io/%s/%s", makeLoxoneUUIDString(le.uuidAction), cmdVal)
}

/* The Loxone server encodes the UUID's as Little Endian, so tranform the bytes
 * and then create a UUID object.
 */
func stateUUID(state []byte) (uu uuid.UUID, err error) {
	orderedBytes := []byte{state[3], state[2], state[1], state[0], state[5], state[4], state[7], state[6]}
	orderedBytes = append(orderedBytes, state[8:16]...)
	return uuid.FromBytes(orderedBytes)
}

func makeLoxoneUUIDString(uu uuid.UUID) string {
	uStr := fmt.Sprintf("%s", uu)
	lI := strings.LastIndex(uStr, "-")
	return uStr[:lI] + uStr[lI+1:]
}

func parseValueState(state []byte, mqChan chan mqttState) {
	for n := 0; n < len(state); {
		uu, err := stateUUID(state[n:])
		if err != nil {
			log.Printf("Error reading UUID from position %d: %s", n, err)
			break
		}
		uval := binary.LittleEndian.Uint64(state[n+16:])
		float := math.Float64frombits(uval)
		log.Printf("valueState: %s -> %f", uu, float)
		n += 24
		_, ck := stateLinks[uu]
		if ck {
			mqChan <- mqttState{uu, fmt.Sprintf("%f", float)}
		} //else {
		//			log.Printf("No match for UUID %s within stateLinks\n", uu)
		//		}
	}
}

func parseTextState(state []byte, mqChan chan mqttState) {
	for n := 0; n < len(state); {
		uu, err := stateUUID(state[n:])
		if err != nil {
			log.Printf("Error reading UUID from position %d: %s", n, err)
			break
		}
		// Skip Icon UUID
		sLen := int(binary.LittleEndian.Uint32(state[n+32:]))
		n += 36
		val := string(state[n : n+sLen])
		n += sLen
		if n%4 != 0 {
			n += (n % 4)
		}
		log.Printf("textState: %s -> %s", uu, val)
		_, ck := stateLinks[uu]
		if ck {
			mqChan <- mqttState{uu, val}
		}
	}
}
