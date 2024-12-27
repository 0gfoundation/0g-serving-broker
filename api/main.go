package main

import (
	"log"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/rand"

	providerEvent "github.com/0glabs/0g-serving-broker/inference/cmd/event"
	providerServer "github.com/0glabs/0g-serving-broker/inference/cmd/server"
)

func main() {
	applets := map[string]func(){
		"0g-inference-server": providerServer.Main,
		"0g-inference-event":  providerEvent.Main,
	}

	names := []string{}
	for k := range applets {
		names = append(names, k)
	}
	appletsHelp := "Currently defined applets: " + strings.Join(names, ", ") + "."

	if len(os.Args) < 2 {
		log.Printf("Usage: %s [applet [arguments]...]\n\n", os.Args[0])
		log.Println(appletsHelp)
		os.Exit(1)
	}
	applet := os.Args[1]
	if f, ok := applets[applet]; ok {
		os.Args = os.Args[1:]
		rand.Seed(time.Now().UnixNano())
		f()
	} else {
		log.Printf("%s: applet not found\n\n", applet)
		log.Println(appletsHelp)
		os.Exit(1)
	}
}
