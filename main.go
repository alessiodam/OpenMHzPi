package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

var MaxQueueSize = 50

type System struct {
	Name        string  `json:"name"`
	ShortName   string  `json:"shortName"`
	SystemType  string  `json:"systemType"`
	City        string  `json:"city"`
	State       string  `json:"state"`
	Active      bool    `json:"active"`
	LastActive  string  `json:"lastActive"`
	CallAvg     float64 `json:"callAvg"`
	Description string  `json:"description"`
}

type SystemsResponse struct {
	Success bool     `json:"success"`
	Systems []System `json:"systems"`
}

type Call struct {
	ID           string `json:"_id"`
	TalkgroupNum int    `json:"talkgroupNum"`
	URL          string `json:"url"`
	Filename     string `json:"filename"`
	Time         string `json:"time"`
	SrcList      []struct {
		Pos float64 `json:"pos"`
		Src string  `json:"src"`
		ID  string  `json:"_id"`
	} `json:"srcList"`
	Star int `json:"star"`
	Freq int `json:"freq"`
	Len  int `json:"len"`
}

type CallsResponse struct {
	Calls []Call `json:"calls"`
}

func initLogger() *logrus.Logger {
	logger := logrus.New()
	logger.SetFormatter(&logrus.TextFormatter{
		ForceColors:   true,
		FullTimestamp: true,
	})
	logger.SetOutput(os.Stdout)
	logger.SetLevel(logrus.InfoLevel)
	return logger
}

func fetchSystems(logger *logrus.Logger) (string, error) {
	logger.Debug("Fetching available systems...")

	proxyURL, err := url.Parse("http://localhost:8191/v1")
	if err != nil {
		return "", fmt.Errorf("error parsing proxy URL: %w", err)
	}

	client := &http.Client{}
	data := map[string]interface{}{
		"cmd":        "request.get",
		"url":        "https://api.openmhz.com/systems",
		"maxTimeout": 60000,
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("error marshalling JSON: %w", err)
	}

	req, err := http.NewRequest("POST", proxyURL.String(), bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("error creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("error fetching systems: %w", err)
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			logger.Error("Error closing response body: ", err)
		}
	}(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to fetch systems: status code %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response: %w", err)
	}

	htmlContent := string(body)
	startIndex := strings.Index(htmlContent, "<pre>")
	endIndex := strings.Index(htmlContent, "</pre>")

	if startIndex == -1 || endIndex == -1 {
		return "", fmt.Errorf("error: <pre> tags not found in response")
	}

	jsonStr := htmlContent[startIndex+len("<pre>") : endIndex]

	unescapedJSON, err := strconv.Unquote(`"` + jsonStr + `"`)
	if err != nil {
		logger.Error("Error unescaping JSON: ", err)
		logger.Error("Raw extracted JSON: ", jsonStr)
		return "", fmt.Errorf("error unescaping JSON: %w", err)
	}

	var systemsResponse SystemsResponse
	if err := json.Unmarshal([]byte(unescapedJSON), &systemsResponse); err != nil {
		return "", fmt.Errorf("error parsing systems JSON: %w", err)
	}

	if !systemsResponse.Success {
		return "", fmt.Errorf("failed to fetch systems: response indicates failure")
	}

	logger.Info("Available systems:")
	for _, system := range systemsResponse.Systems {
		fmt.Printf("- %s: %s\n", system.Name, system.ShortName)
	}

	fmt.Print("Enter the shortName of the system you want to use: ")
	var shortName string
	_, err = fmt.Scanln(&shortName)
	if err != nil {
		return "", err
	}

	return shortName, nil
}

func fetchCalls(logger *logrus.Logger, systemShortName string, queue chan Call, processedCalls *sync.Map) {
	logger.Debugf("Fetching calls for system: %s", systemShortName)
	urlStr := fmt.Sprintf("https://api.openmhz.com/%s/calls", systemShortName)

	proxyURL, err := url.Parse("http://localhost:8191/v1")
	if err != nil {
		logger.Error("Error parsing proxy URL: ", err)
		return
	}

	client := &http.Client{}

	firstRun := true
	for {
		time.Sleep(5 * time.Second)
		data := map[string]interface{}{
			"cmd":        "request.get",
			"url":        urlStr,
			"maxTimeout": 60000,
		}
		jsonData, err := json.Marshal(data)
		if err != nil {
			logger.Error("Error marshalling JSON: ", err)
			continue
		}

		req, err := http.NewRequest("POST", proxyURL.String(), bytes.NewBuffer(jsonData))
		if err != nil {
			logger.Error("Error creating request: ", err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			logger.Error("Error fetching calls: ", err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			logger.Error("Error fetching calls: status code ", resp.StatusCode)
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		err = resp.Body.Close()
		if err != nil {
			logger.Error("Error closing response body: ", err)
			continue
		}

		htmlContent := string(body)
		startIndex := strings.Index(htmlContent, "<pre>")
		endIndex := strings.Index(htmlContent, "</pre>")

		if startIndex != -1 && endIndex != -1 {
			jsonStr := htmlContent[startIndex+len("<pre>") : endIndex]

			unescapedJSON, err := strconv.Unquote(`"` + jsonStr + `"`)
			if err != nil {
				logger.Error("Error unescaping JSON: ", err)
				logger.Error("Raw extracted JSON: ", jsonStr)
				continue
			}

			var response CallsResponse
			if err := json.Unmarshal([]byte(unescapedJSON), &response); err != nil {
				logger.Error("Error parsing unescaped JSON: ", err)
				logger.Error("Unescaped JSON: ", unescapedJSON)
				continue
			}

			var callsToAdd []Call
			for _, call := range response.Calls {
				if _, exists := processedCalls.Load(call.ID); exists {
					continue
				}
				if firstRun {
					processedCalls.Store(call.ID, true)
					continue
				}

				logger.Debug("New call detected, adding to processed calls: ", call.Filename)
				processedCalls.Store(call.ID, true)
				callsToAdd = append(callsToAdd, call)
			}

			for i := len(callsToAdd) - 1; i >= 0; i-- {
				select {
				case queue <- callsToAdd[i]:
					logger.Info("Call added to queue: ", callsToAdd[i].ID)
				default:
					logger.Warn("Queue is full, removing oldest call to add new one: ", callsToAdd[i].Filename)
					<-queue
					queue <- callsToAdd[i]
				}
			}
			logger.Infof("Fetched %d new calls, %d/%d items in queue", len(callsToAdd), len(queue), MaxQueueSize)
			firstRun = false
		} else {
			logger.Error("Error: <pre> tags not found in response.")
		}
	}
}

func playAudio(logger *logrus.Logger, queue <-chan Call) {
	for call := range queue {
		logger.Info("Processing audio: ", call.Filename)

		m4aPath := "OpenMHzPi-downloads/" + strings.Split(call.Filename, "/")[len(strings.Split(call.Filename, "/"))-1]
		mp3Path := strings.Replace(m4aPath, ".m4a", ".mp3", 1)

		logger.Debug("Downloading audio from URL: ", call.URL)
		err := downloadFile(call.URL, m4aPath)
		if err != nil {
			logger.Error("Error downloading file: ", err)
			continue
		}

		logger.Debug("Converting audio to MP3: ", mp3Path)
		convertCmd := exec.Command("ffmpeg", "-i", m4aPath, mp3Path, "-y")
		err = convertCmd.Run()
		if err != nil {
			logger.Error("Error converting to MP3: ", err)
			continue
		}

		logger.Debug("Playing MP3 file: ", mp3Path)
		playCmd := exec.Command("mpg123", mp3Path)
		err = playCmd.Run()
		if err != nil {
			logger.Error("Error playing audio: ", err)
			continue
		}

		logger.Debug("Cleaning up files.")
		err = os.Remove(m4aPath)
		if err != nil {
			logger.Error("Error deleting M4A file: ", err)
		}
		err = os.Remove(mp3Path)
		if err != nil {
			logger.Error("Error deleting MP3 file: ", err)
		}

		logger.Infof("Completed processing: %s, %d/%d items left in queue", call.Filename, len(queue), MaxQueueSize)
	}
}

func downloadFile(url, filepath string) error {
	out, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer func(out *os.File) {
		_ = out.Close()
	}(out)

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}

	return nil
}

func main() {
	logger := initLogger()

	err := os.RemoveAll("OpenMHzPi-downloads")
	if err != nil {
		logger.Error("Error deleting OpenMHzPi-downloads directory: ", err)
		return
	}
	err = os.MkdirAll("OpenMHzPi-downloads", os.ModePerm)
	if err != nil {
		logger.Error("Error creating OpenMHzPi-downloads directory: ", err)
		return
	}

	rootCmd := &cobra.Command{Use: "app"}
	var shortName string
	var debug bool

	rootCmd.PersistentFlags().StringVar(&shortName, "shortname", "", "Short name of the system")
	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "Enable debug mode")

	rootCmd.Run = func(cmd *cobra.Command, args []string) {
		if debug {
			logger.SetLevel(logrus.DebugLevel)
			logger.Info("Debug mode enabled")
		}

		if shortName == "" {
			shortName, err = fetchSystems(logger)
			if err != nil {
				logger.Fatal("Failed to select a system: ", err)
				return
			}
		} else {
			logger.Infof("Using provided system shortname: %s", shortName)
		}

		queue := make(chan Call, MaxQueueSize)
		processedCalls := &sync.Map{}

		go fetchCalls(logger, shortName, queue, processedCalls)
		go playAudio(logger, queue)

		select {}
	}

	if err := rootCmd.Execute(); err != nil {
		logger.Fatal(err)
	}
}
