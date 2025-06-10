// 0g-serving-broker CLI tool
// Supports the list-providers command, switching between inference and fine-tuning via the --infer flag
// Usage examples:
//   go run main.go list-providers --infer=true
//   go run main.go list-providers --infer=false
//   go run main.go --help
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const (
	inferenceURL   = "http://localhost:8081/v1/user"   // Inference service API endpoint
	finetuningURL  = "http://localhost:8080/v1/user"   // Fine-tuning service API endpoint
)

// printHelp prints the usage instructions (in English)
func printHelp() {
	help := `Usage:
  list-providers --infer=[true|false]   # Query inference or fine-tuning provider list
  --help                               # Show help
`
	fmt.Print(help)
}

// main is the entry point of the CLI tool
func main() {
	// If no command or help is requested, print help and exit
	if len(os.Args) < 2 || os.Args[1] == "--help" || os.Args[1] == "help" {
		printHelp()
		return
	}

	cmd := os.Args[1]
	// Set up a flag set for the list-providers command
	inferFlag := flag.NewFlagSet("list-providers", flag.ExitOnError)
	infer := inferFlag.Bool("infer", false, "Whether to query inference providers (true for inference, false for fine-tuning)")

	if cmd == "list-providers" {
		_ = inferFlag.Parse(os.Args[2:])
		var url string
		// Choose the API endpoint based on the --infer flag
		if *infer {
			url = inferenceURL
		} else {
			url = finetuningURL
		}
		// Send HTTP GET request to the selected endpoint
		resp, err := http.Get(url)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Request failed: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		// Check for non-200 HTTP status
		if resp.StatusCode != 200 {
			fmt.Fprintf(os.Stderr, "HTTP error: %d %s\n", resp.StatusCode, resp.Status)
			os.Exit(1)
		}
		// Read the response body
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to read response: %v\n", err)
			os.Exit(1)
		}
		// Pretty-print the JSON response
		var pretty strings.Builder
		if err := json.Indent(&pretty, body, "", "  "); err != nil {
			fmt.Fprintf(os.Stderr, "JSON formatting failed: %v\nOriginal content: %s\n", err, string(body))
			os.Exit(1)
		}
		fmt.Println(pretty.String())
		return
	}

	// Unknown command: print error and help
	fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
	printHelp()
	os.Exit(1)
} 