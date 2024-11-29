package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	MaxQueueSize  = 50
	FetchInterval = 5 * time.Second
)

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
	ID       string `json:"_id"`
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Time     string `json:"time"`
}

type CallsResponse struct {
	Calls []Call `json:"calls"`
}

func initLogger(debug bool) *logrus.Logger {
	logger := logrus.New()
	logger.SetFormatter(&logrus.TextFormatter{
		ForceColors:   true,
		FullTimestamp: true,
	})
	logger.SetOutput(os.Stdout)
	logger.SetLevel(logrus.InfoLevel)
	if debug {
		logger.SetLevel(logrus.DebugLevel)
	}
	return logger
}

func fetchJSON(logger *logrus.Logger, proxyURL, targetURL string) ([]byte, error) {
	logger.Debugf("Fetching JSON via proxy. Target URL: %s", targetURL)

	client := &http.Client{}

	requestData := map[string]interface{}{
		"cmd":        "request.get",
		"url":        targetURL,
		"maxTimeout": 60000,
	}

	jsonData, err := json.Marshal(requestData)
	if err != nil {
		return nil, fmt.Errorf("error marshalling request JSON: %w", err)
	}

	req, err := http.NewRequest("POST", proxyURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error performing request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			logger.Warnf("Failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response body: %w", err)
	}

	htmlContent := string(body)
	startIndex := strings.Index(htmlContent, "<pre>")
	endIndex := strings.Index(htmlContent, "</pre>")

	if startIndex == -1 || endIndex == -1 {
		return nil, fmt.Errorf("failed to locate <pre> tags in response")
	}

	jsonStr := htmlContent[startIndex+len("<pre>") : endIndex]

	unescapedJSON, err := strconv.Unquote(`"` + jsonStr + `"`)
	if err != nil {
		logger.Errorf("Error unescaping JSON: %v", err)
		logger.Errorf("Raw JSON: %s", jsonStr)
		return nil, fmt.Errorf("error unescaping JSON: %w", err)
	}

	return []byte(unescapedJSON), nil
}

func fetchSystems(logger *logrus.Logger, proxyURL string) (string, error) {
	body, err := fetchJSON(logger, proxyURL, "https://api.openmhz.com/systems")
	if err != nil {
		return "", fmt.Errorf("failed to fetch systems: %w", err)
	}

	var systemsResponse SystemsResponse
	if err := json.Unmarshal(body, &systemsResponse); err != nil {
		return "", fmt.Errorf("error parsing systems JSON: %w", err)
	}

	if !systemsResponse.Success {
		return "", fmt.Errorf("API response indicates failure")
	}

	logger.Info("Available systems:")
	for _, system := range systemsResponse.Systems {
		logger.Infof("- %s (%s)", system.Name, system.ShortName)
	}

	fmt.Print("Enter the shortName of the system you want to use: ")
	var shortName string
	if _, err := fmt.Scanln(&shortName); err != nil {
		return "", fmt.Errorf("error reading user input: %w", err)
	}

	return shortName, nil
}

func fetchCalls(logger *logrus.Logger, proxyURL, systemShortName string, queue chan Call, processedCalls *sync.Map, done <-chan struct{}) {
	apiURL := fmt.Sprintf("https://api.openmhz.com/%s/calls", systemShortName)
	logger.Debugf("API URL: %s", apiURL)

	isFirstRun := true

	for {
		select {
		case <-done:
			logger.Info("Stopping call fetcher.")
			return
		case <-time.After(FetchInterval):
			logger.Debug("Fetching calls...")
			body, err := fetchJSON(logger, proxyURL, apiURL)
			if err != nil {
				logger.Error("Error fetching calls: ", err)
				continue
			}

			logger.Debugf("Fetched calls JSON: %s", string(body))
			var callsResponse CallsResponse
			if err := json.Unmarshal(body, &callsResponse); err != nil {
				logger.Error("Error parsing calls JSON: ", err)
				continue
			}

			logger.Debugf("Parsed %d calls", len(callsResponse.Calls))

			for _, call := range callsResponse.Calls {
				logger.Debugf("Processing call ID: %s", call.ID)

				if isFirstRun {
					processedCalls.Store(call.ID, true)
					logger.Infof("Marked call ID %s as processed (initial run)", call.ID)
					continue
				}

				if _, exists := processedCalls.LoadOrStore(call.ID, true); !exists {
					select {
					case queue <- call:
						logger.Infof("New call added to queue: %s", call.ID)
					default:
						logger.Warn("Queue full, dropping oldest call.")
						<-queue
						queue <- call
					}
				} else {
					logger.Debugf("Call ID %s already processed", call.ID)
				}
			}

			if isFirstRun {
				isFirstRun = false
			}
		}
	}
}

func playAudio(logger *logrus.Logger, queue <-chan Call, done <-chan struct{}) {
	for {
		select {
		case <-done:
			logger.Info("Stopping audio player.")
			return
		case call := <-queue:
			logger.Infof("Processing call: %s", call.ID)

			err := playRogerBeep()
			if err != nil {
				logger.Error("Failed to play walkie-talkie beep: ", err)
				continue
			}

			if err := playFile(call.URL); err != nil {
				logger.Error("Failed to play file: ", err)
				continue
			}
		}
	}
}

func playRogerBeep() error {
	cmd := exec.Command("ffplay", "-autoexit", "-nodisp", "-f", "lavfi",
		"-i", "sine=frequency=1000:duration=0.2 [s0]; sine=frequency=1500:duration=0.2 [s1]; [s0][s1]concat=n=2:v=0:a=1 [out0]")
	return cmd.Run()
}

func playFile(url string) error {
	cmd := exec.Command("ffplay", "-autoexit", "-nodisp", url)
	return cmd.Run()
}

func isFlareSolverrRunning() bool {
	resp, err := http.Get("http://localhost:8191/")
	if err != nil {
		return false
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)
	return resp.StatusCode == http.StatusOK
}

func main() {
	var shortName string
	var debug bool

	rootCmd := &cobra.Command{
		Use: "app",
		Run: func(cmd *cobra.Command, args []string) {
			logger := initLogger(debug)

			proxyURL := "http://localhost:8191/v1"

			if !isFlareSolverrRunning() {
				logger.Fatal("FlareSolverr is not running. Please start it before running this application.")
			}

			if shortName == "" {
				var err error
				shortName, err = fetchSystems(logger, proxyURL)
				if err != nil {
					logger.Fatal(err)
				}
			}

			queue := make(chan Call, MaxQueueSize)
			processedCalls := &sync.Map{}
			done := make(chan struct{})

			go fetchCalls(logger, proxyURL, shortName, queue, processedCalls, done)
			go playAudio(logger, queue, done)

			c := make(chan os.Signal, 1)
			signal.Notify(c, os.Interrupt, syscall.SIGTERM)
			<-c

			logger.Info("Shutting down...")
			close(done)
			time.Sleep(2 * time.Second)
		},
	}

	rootCmd.PersistentFlags().StringVar(&shortName, "shortname", "", "Short name of the system")
	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "Enable debug mode")

	if err := rootCmd.Execute(); err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
}
