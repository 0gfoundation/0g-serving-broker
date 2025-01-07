package util

import (
	"archive/zip"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func GenerateRandomString() (string, error) {
	randomBytes := make([]byte, 16)
	_, err := rand.Read(randomBytes)
	if err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %v", err)
	}

	return hex.EncodeToString(randomBytes), nil
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
		header.Name = sourceDir

		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			return err
		}

		_, err = io.Copy(writer, source)
		return err
	}
}

func UploadEncryptFile(sourceDir string, ciphertext []byte, tagSig []byte) ([]byte, error) {
	encryptFile, err := GetFileName(sourceDir, ".data")
	if err != nil {
		return nil, err
	}

	err = os.WriteFile(encryptFile, append(ciphertext, tagSig...), 0644)

	defer func() {
		_, err := os.Stat(encryptFile)
		if err != nil && os.IsNotExist(err) {
			return
		}
		_ = os.Remove(encryptFile)
	}()

	if err != nil {
		return nil, err
	}

	// todo: upload encryptFile and get model root hash
	modelRootHash := []byte{}

	return modelRootHash, nil
}
