package main

import (
	"log"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/rand"

	providerEvent "github.com/0glabs/0g-serving-agent/provider/cmd/event"
	providerServer "github.com/0glabs/0g-serving-agent/provider/cmd/server"
	userEvent "github.com/0glabs/0g-serving-agent/user/cmd/event"
	userServer "github.com/0glabs/0g-serving-agent/user/cmd/server"
)

func main() {
	applets := map[string]func(){
		"0g-provider-server": providerServer.Main,
		"0g-provider-event":  providerEvent.Main,
		"0g-user-server":     userServer.Main,
		"0g-user-event":      userEvent.Main,
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
