package main

import (
	"fmt"
	"io/ioutil"

	"gopkg.in/yaml.v2"
)

type yamlConfig struct {
	Loxone struct {
		Host     string
		Port     int
		Username string
		Password string
	}
	MQTT struct {
		Host  string
		Port  int
		Topic string
	}
	Logging struct {
		File   string
		Syslog bool
	}
}

func parseConfigFile(filename string) (cfg yamlConfig, err error) {
	yamlFile, err := ioutil.ReadFile(filename)
	if err != nil {
		err = fmt.Errorf("Unable to read configuration file %s", filename)
		return
	}

	err = yaml.Unmarshal(yamlFile, &cfg)
	if err != nil {
		fmt.Printf("Unable to parse the YAML file :-( %san", err)
		return
	}
	return
}
