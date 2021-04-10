package main

import (
	"fmt"
	"log"
	"strings"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/google/uuid"
)

type mqttState struct {
	uuid  uuid.UUID
	value string
}

var (
	client        mqtt.Client
	mqttChannel   chan mqttState
	actionChannel chan string
)

func startMQTT(host string, port int) (chan mqttState, chan string, error) {
	mqOpts := mqtt.NewClientOptions()
	mqOpts.AddBroker(fmt.Sprintf("tcp://%s:%d", host, port))
	mqOpts.OnConnect = mqttConnect
	mqOpts.SetDefaultPublishHandler(actionHandler)

	client = mqtt.NewClient(mqOpts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		return nil, nil, fmt.Errorf("Unable to connect to the MQTT server on bob: %v\n", token.Error())
	}
	mqttChannel = make(chan mqttState, 2)
	actionChannel = make(chan string, 2)
	go mqttPublisher()
	return mqttChannel, actionChannel, nil
}

var mqttConnect mqtt.OnConnectHandler = func(c mqtt.Client) {
	if token := c.Subscribe("loxone/+/action", 1, nil); token.Wait() && token.Error() != nil {
		log.Printf("Unable to subscribe to required topics: %s", token.Error())
	} else {
		log.Print("MQTT connected & subscribed OK")
	}
}

func mqttPublisher() {
	for {
		msg := <-mqttChannel
		topic := fmt.Sprintf("loxone/%s/state", msg.uuid)
		token := client.Publish(topic, byte(0), true, msg.value)
		token.Wait()
		if token.Error() != nil {
			log.Printf("Error publishing state -> %v", msg)
			break
		}
		log.Printf("Publish: %s -> %s\n", topic, msg.value)
	}
	client.Disconnect(0)
}

var actionHandler mqtt.MessageHandler = func(client mqtt.Client, msg mqtt.Message) {
	log.Printf("Received MQTT message: %v", msg)

	parts := strings.Split(msg.Topic(), "/")
	reqUUID, err := uuid.Parse(parts[1])
	if err != nil {
		log.Printf("Error decoding %s as UUID", parts[1])
		return
	}
	le, ck := actionLinks[reqUUID]
	if !ck {
		log.Printf("Unknown action UUID '%s'", reqUUID)
		return
	}
	log.Printf("Action requested for %s [%s]", le.Name, string(msg.Payload()))
	actionChannel <- le.actionCommand(msg.Payload())
}
