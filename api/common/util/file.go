package util

import (
	"archive/zip"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

func GenerateRandomString() (string, error) {
	randomBytes := make([]byte, 8)
	_, err := rand.Read(randomBytes)
	if err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %v", err)
	}

	return hexutil.Encode(randomBytes)[2:], nil
}

func GetFileName(prefix, extension string) (string, error) {
	for {
		fileName, error := GenerateRandomString()
		if error != nil {
			return "", error
		}

		zipFile := prefix + fileName + extension
		_, err := os.Stat(zipFile)
		if err != nil {
			if os.IsNotExist(err) {
				return zipFile, nil
			}

			return "", err
		}
	}
}

func Zip(sourceDir string) (string, error) {
	zipFile, err := GetFileName(sourceDir, ".zip")
	if err != nil {
		return "", err
	}

	err = ZipDirectory(sourceDir, zipFile)
	if err != nil {
		return "", err
	}

	return zipFile, nil
}

func ZipAndGetContent(sourceDir string) ([]byte, error) {
	zipFile, err := GetFileName(sourceDir, ".zip")
	if err != nil {
		return nil, err
	}

	err = ZipDirectory(sourceDir, zipFile)

	defer func() {
		_, err := os.Stat(zipFile)
		if err != nil && os.IsNotExist(err) {
			return
		}
		_ = os.Remove(zipFile)
	}()

	if err != nil {
		return nil, err
	}

	plaintext, err := os.ReadFile(zipFile)
	if err != nil {
		return nil, fmt.Errorf("could not read file %s: %v", zipFile, err)
	}

	return plaintext, nil
}

func ZipDirectory(sourceDir, destinationZip string) error {
	zipFile, err := os.Create(destinationZip)
	if err != nil {
		return err
	}
	defer zipFile.Close()

	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	info, err := os.Stat(sourceDir)
	if err != nil {
		return err
	}

	if info.IsDir() {
		err = filepath.Walk(sourceDir, func(file string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if file == sourceDir {
				return nil
			}

			if info.IsDir() {
				return nil
			}

			f, err := os.Open(file)
			if err != nil {
				return err
			}
			defer f.Close()

			header, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}

			relPath, err := filepath.Rel(sourceDir, file)
			if err != nil {
				return err
			}
			header.Name = relPath

			writer, err := zipWriter.CreateHeader(header)
			if err != nil {
				return err
			}

			_, err = io.Copy(writer, f)
			return err
		})

		return err
	} else {
		source, err := os.Open(sourceDir)
		if err != nil {
			return err
		}
		defer source.Close()

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}

		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			return err
		}

		_, err = io.Copy(writer, source)
		return err
	}
}

func WriteToFile(sourceDir string, ciphertext []byte, tagSig []byte) (string, error) {
	encryptFile, err := GetFileName(sourceDir, ".data")
	if err != nil {
		return "", err
	}

	err = os.WriteFile(encryptFile, append(ciphertext, tagSig...), 0644)

	if err != nil {
		return "", err
	}

	return encryptFile, nil
}

func WriteToFileHead(filename string, tagSig []byte) error {
	file, err := os.OpenFile(filename, os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open file: %v", err)
	}
	defer file.Close()

	_, err = file.Write(tagSig)
	if err != nil {
		return fmt.Errorf("failed to write data: %v", err)
	}

	return nil
}

func FileContentSize(filePath string) (int64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, errors.Wrap(err, "opening file")
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		return 0, errors.Wrap(err, "getting file info")
	}

	return fileInfo.Size(), nil
}

// Unzip extracts a ZIP archive to a specified destination folder.
func Unzip(src string, dest string) error {
	// Open the zip file
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	// Ensure the destination folder exists
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}

	// Extract each file from the zip archive
	for _, f := range r.File {
		filePath := filepath.Join(dest, f.Name)

		// Ensure the path is safe (prevent directory traversal)
		if !filepath.HasPrefix(filePath, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path: %s", filePath)
		}

		// If it's a directory, create it
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(filePath, f.Mode()); err != nil {
				return err
			}
			continue
		}

		// Create the file
		if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			return err
		}

		outFile, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		// Open the file in the zip archive
		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return err
		}

		// Copy the contents of the file
		_, err = io.Copy(outFile, rc)

		// Close resources
		outFile.Close()
		rc.Close()

		if err != nil {
			return err
		}
	}

	return nil
}
