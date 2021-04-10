package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/google/uuid"
)

var stateLinks map[uuid.UUID]*loxoneEntity
var actionLinks map[uuid.UUID]*loxoneEntity

func main() {
	var cfgFile string
	var hass bool

	flag.StringVar(&cfgFile, "cfg", "configuration.yaml", "Configuration file to use")
	flag.BoolVar(&hass, "hass", false, "Display HASS configuration yaml")
	flag.Parse()

	cfg, err := parseConfigFile(cfgFile)
	if err != nil {
		panic(err)
	}

	if len(cfg.Logging.File) > 0 {
		file, err := os.OpenFile(cfg.Logging.File, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			panic(fmt.Errorf("Unable to open log file %s to use: %s", cfg.Logging.File, err))
		}
		log.SetOutput(file)
		defer file.Close()
	}
	/*
		if cfg.Logging.Syslog {
			syslogger, err := syslog.New(syslog.LOG_INFO, "halox")
			if err != nil {
				log.Fatalln(err)
			}

			log.SetOutput(syslogger)
		}
	*/

	log.Print("halox starting")

	stateLinks = make(map[uuid.UUID]*loxoneEntity)
	actionLinks = make(map[uuid.UUID]*loxoneEntity)

	ls := newLoxoneServer(cfg.Loxone.Host, cfg.Loxone.Port, cfg.Loxone.Username, cfg.Loxone.Password)

	err = ls.connect()
	if err != nil {
		log.Print(err)
		fmt.Printf("%s\n", err)
		return
	}

	structure, err := ls.getStructureFile()
	if err != nil {
		log.Println(err)
		return
	}

	for uu, data := range structure["controls"].(map[string]interface{}) {
		le := newLoxoneEntity(uu, data.(map[string]interface{}))

		for _, uuid := range le.states {
			stateLinks[uuid] = &le
		}
		actionLinks[le.uuidAction] = &le
	}

	if hass {
		fmt.Println("Need to display HASS YAML...")
		fmt.Println("switch:")
		for _, le := range actionLinks {
			fmt.Println(le.hassYaml())
		}
		os.Exit(0)
	}

	mqChan, actionChannel, err := startMQTT(cfg.MQTT.Host, cfg.MQTT.Port)
	if err != nil {
		log.Print(err)
		return
	}

	if err := ls.enableUpdates(); err != nil {
		fmt.Println(err)
		return
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

mainLoop:
	for {
		select {
		case msg := <-ls.updateChannel:
			switch msg.MsgType {
			case 2:
				parseValueState(msg.Data, mqChan)
			case 3:
				parseTextState(msg.Data, mqChan)
			default:
				log.Printf("Received update packet of type %d, ignoring...", msg.MsgType)
			}
		case cmd := <-actionChannel:
			ls.sendCommand(cmd)
		case <-sigs:
			log.Print("Signal received, exiting...")
			break mainLoop
		}
	}

	return
}
