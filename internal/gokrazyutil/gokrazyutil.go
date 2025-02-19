package gokrazyutil

import (
	"fmt"
	"io/ioutil"
	"os"
	"strings"
)

func ReadConfigFile(fileName string) (string, error) {
	str, err := ioutil.ReadFile("/perm/" + fileName)
	if err != nil {
		str, err = ioutil.ReadFile("/etc/" + fileName)
	}
	if err != nil && os.IsNotExist(err) {
		str, err = ioutil.ReadFile("/" + fileName)
	}

	return strings.TrimSpace(string(str)), err
}

func DashboardURL() (string, error) {
	httpPassword, err := ReadConfigFile("gokr-pw.txt")
	if err != nil {
		return "", fmt.Errorf("failed to read http password: %w", err)
	}

	httpPort, err := ReadConfigFile("http-port.txt")
	if err != nil {
		return "", fmt.Errorf("failed to read http port: %w", err)
	}

	return fmt.Sprintf("http://gokrazy:%s@127.0.0.1:%s/", httpPassword, httpPort), nil
}
