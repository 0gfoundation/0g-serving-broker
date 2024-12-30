package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

func camelToSnakeCase(s string) string {
	regex := regexp.MustCompile("([a-z])([A-Z])")
	snake := regex.ReplaceAllString(s, "${1}_${2}")
	return strings.ToLower(snake)
}

func main() {
	err := extractAndGenerate()
	if err != nil {
		fmt.Println("Error:", err)
	}
}

func extractAndGenerate() error {
	mainFolder := "../../libs/0g-serving-contract/artifacts/contracts/fine-tune"
	abiFolder := "abis"

	folders, err := os.ReadDir(mainFolder)
	if err != nil {
		return fmt.Errorf("failed to read directory %s: %v", mainFolder, err)
	}

	for _, folder := range folders {
		if folder.IsDir() && strings.Contains(folder.Name(), ".sol") {
			absolutePath := filepath.Join(mainFolder, folder.Name())

			files, err := os.ReadDir(absolutePath)
			if err != nil {
				return fmt.Errorf("failed to read directory %s: %v", absolutePath, err)
			}

			for _, file := range files {
				if !strings.Contains(file.Name(), ".dbg.json") {
					finalPath := filepath.Join(absolutePath, file.Name())

					// Create the ABI folder if it doesn't exist
					if _, err := os.Stat(abiFolder); os.IsNotExist(err) {
						err := os.Mkdir(abiFolder, 0755)
						if err != nil {
							return fmt.Errorf("failed to create directory %s: %v", abiFolder, err)
						}
					}

					data, err := os.ReadFile(finalPath)
					if err != nil {
						return fmt.Errorf("failed to read file %s: %v", finalPath, err)
					}

					var jsonData map[string]interface{}
					err = json.Unmarshal(data, &jsonData)
					if err != nil {
						return fmt.Errorf("failed to parse JSON from file %s: %v", finalPath, err)
					}

					abi, ok := jsonData["abi"]
					if !ok {
						return fmt.Errorf("ABI not found in %s", finalPath)
					}

					abiData, err := json.Marshal(abi)
					if err != nil {
						return fmt.Errorf("failed to serialize ABI from file %s: %v", finalPath, err)
					}

					abiFilePath := filepath.Join(abiFolder, file.Name())
					err = os.WriteFile(abiFilePath, abiData, 0644)
					if err != nil {
						return fmt.Errorf("failed to write ABI to file %s: %v", abiFilePath, err)
					}

					// Generate Go contract file using abigen
					pkgName := "contract"
					typeName := strings.TrimSuffix(file.Name(), filepath.Ext(file.Name()))
					outputFilePath := camelToSnakeCase(typeName) + ".go"

					cmd := exec.Command("abigen", "--abi", abiFilePath, "--pkg", pkgName, "--type", typeName, "--out", outputFilePath)
					var out bytes.Buffer
					var stderr bytes.Buffer
					cmd.Stdout = &out
					cmd.Stderr = &stderr
					err = cmd.Run()
					if err != nil {
						return fmt.Errorf("failed to run abigen command: %v, %s", err, stderr.String())
					}
				}
			}
		}
	}

	err = os.RemoveAll(abiFolder)
	if err != nil {
		return fmt.Errorf("failed to delete directory %s: %v", abiFolder, err)
	}

	return nil
}
