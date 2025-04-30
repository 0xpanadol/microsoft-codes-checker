package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"
)

const (
	baseURL                 = "https://buynow.production.store-web.dynamics.com/v1.0/Redeem/PrepareRedeem"
	outputDir               = "output"
	inputDir                = "input"
	codesFile               = "codes.txt"
	wlidsFile               = "wlids.txt"
	wlidRemovedFile         = "wlids_removed_history.txt"
	wlidActiveFinalFile     = "wlids_active_final.txt"
	wlidRemovedFilePath     = outputDir + "/" + wlidRemovedFile
	wlidActiveFinalFilePath = outputDir + "/" + wlidActiveFinalFile
)

type CheckRequest struct {
	Market                   string   `json:"market"`
	Language                 string   `json:"language"`
	Flights                  []string `json:"flights"`
	TokenIdentifierValue     string   `json:"tokenIdentifierValue"`
	SupportsCsvTypeTokenOnly bool     `json:"supportsCsvTypeTokenOnly"`
	BuyNowScenario           string   `json:"buyNowScenario"`
	ClientContext            struct {
		Client       string `json:"client"`
		DeviceFamily string `json:"deviceFamily"`
	} `json:"clientContext"`
}
type CheckResponse struct {
	Events struct {
		Cart []struct {
			Data struct {
				Reason      string `json:"reason"`
				IsUserError bool   `json:"isUserError"`
			} `json:"data"`
			Type     string `json:"type"`
			Code     string `json:"code"`
			Provider string `json:"provider"`
		} `json:"cart"`
	} `json:"events"`
	TokenType string      `json:"tokenType"`
	Value     interface{} `json:"value"`
	Products  []struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"products"`
}
type Result struct {
	Code          string
	Status        string
	Details       string
	IsTimeout     bool
	IsRateLimited bool
}

func createDirsAndFiles() error {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create dir %s: %v", outputDir, err)
	}
	if err := os.MkdirAll(inputDir, 0755); err != nil {
		return fmt.Errorf("failed to create dir %s: %v", inputDir, err)
	}

	// Ensure essential input files exist (create empty if not)
	inputFiles := []string{filepath.Join(inputDir, codesFile), filepath.Join(inputDir, wlidsFile)}
	for _, fp := range inputFiles {
		if _, err := os.Stat(fp); os.IsNotExist(err) {
			fmt.Printf("Creating empty file: %s\n", fp)
			f, createErr := os.Create(fp)
			if createErr != nil {
				return fmt.Errorf("failed to create %s: %v", fp, createErr)
			}
			f.Close()
		}
	}

	// Ensure output files exist (removed history and final active)
	outputFiles := []string{wlidRemovedFilePath, wlidActiveFinalFilePath}
	for _, fp := range outputFiles {
		// Append/Create for removed history, Truncate/Create for final active
		flags := os.O_APPEND | os.O_CREATE | os.O_WRONLY
		if fp == wlidActiveFinalFilePath {
			flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC // Overwrite active file
		}
		f, err := os.OpenFile(fp, flags, 0644)
		if err != nil {
			return fmt.Errorf("failed to create/open %s: %v", fp, err)
		}
		f.Close()
	}
	return nil
}

// File Mutexes
var (
	goodFileMutex        sync.Mutex
	badFileMutex         sync.Mutex
	errorFileMutex       sync.Mutex
	expiredFileMutex     sync.Mutex
	retryFileMutex       sync.Mutex
	redeemedFileMutex    sync.Mutex
	wlidRemovedFileMutex sync.Mutex
)

// WLID Management
var (
	activeWlidList []string
	wlidListMutex  sync.Mutex
)

func appendToFile(filename string, content string, mutex *sync.Mutex) error {
	mutex.Lock()
	defer mutex.Unlock()
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.WriteString(content); err != nil {
		return err
	}
	return nil
}

// removeWlidAndLog: Removes from active list IN MEMORY and logs to history file
func removeWlidAndLog(wlidToRemove string, reason string) {
	if wlidToRemove == "" {
		return
	}
	wlidListMutex.Lock() // Lock for modifying activeWlidList

	found := false
	newActiveList := make([]string, 0, len(activeWlidList))
	for _, wlid := range activeWlidList {
		if wlid == wlidToRemove {
			found = true
		} else {
			newActiveList = append(newActiveList, wlid)
		}
	}

	// Only update if found and log to history file
	if found {
		activeWlidList = newActiveList
		wlidListMutex.Unlock() // Unlock BEFORE logging to file (uses different mutex)

		contentToLog := fmt.Sprintf("%s | Reason: %s | Timestamp: %s\n", wlidToRemove, reason, time.Now().Format(time.RFC3339))
		// Append to the history file
		err := appendToFile(wlidRemovedFilePath, contentToLog, &wlidRemovedFileMutex)
		if err != nil {
			fmt.Printf("\n[Error] Failed to write removed WLID %s to history %s: %v\n", wlidToRemove, wlidRemovedFilePath, err)
		}
	} else {
		wlidListMutex.Unlock() // Unlock if not found
	}
}

// Writes the current activeWlidList to the specified file, overwriting it.
func saveActiveWlids(filePath string) error {
	wlidListMutex.Lock() // Lock to safely read the final list
	defer wlidListMutex.Unlock()

	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open %s for writing active WLIDs: %v", filePath, err)
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	count := 0
	for _, wlid := range activeWlidList {
		if _, err := writer.WriteString(wlid + "\n"); err != nil {
			// Try to flush what we have before returning error
			_ = writer.Flush()
			return fmt.Errorf("failed to write WLID %s to %s: %v", wlid, filePath, err)
		}
		count++
	}

	// Flush remaining buffer
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush writer for %s: %v", filePath, err)
	}

	fmt.Printf("Saved %d active WLIDs to %s.\n", count, filePath)
	return nil
}

func getRandomWlid() string {
	wlidListMutex.Lock()
	defer wlidListMutex.Unlock()
	if len(activeWlidList) == 0 {
		return ""
	}
	return activeWlidList[rand.Intn(len(activeWlidList))]
}

func saveResult(result Result) error {
	var filename string
	var mutex *sync.Mutex
	content := result.Code
	switch result.Status {
	case "good":
		filename = filepath.Join(outputDir, "good.txt")
		mutex = &goodFileMutex
		content = fmt.Sprintf("%s | %s", result.Code, result.Details)
	case "bad":
		filename = filepath.Join(outputDir, "bad.txt")
		mutex = &badFileMutex
	case "expired":
		filename = filepath.Join(outputDir, "expired.txt")
		mutex = &expiredFileMutex
	case "retry":
		filename = filepath.Join(outputDir, "retry.txt")
		mutex = &retryFileMutex
		reason := result.Details
		if result.IsTimeout {
			reason = "Network Timeout"
		} else if result.IsRateLimited {
			reason = "Rate Limited"
		}
		content = fmt.Sprintf("%s | %s", result.Code, reason)
	case "redeemed":
		filename = filepath.Join(outputDir, "redeemed.txt")
		mutex = &redeemedFileMutex
	default:
		filename = filepath.Join(outputDir, "error.txt")
		mutex = &errorFileMutex
		content = fmt.Sprintf("%s | %s", result.Code, result.Details)
	}
	return appendToFile(filename, content+"\n", mutex)
}

func checkCode(client *http.Client, code string, wlidToUse string) Result {
	code = strings.TrimSpace(code)
	if code == "" {
		return Result{Code: code, Status: "error", Details: "Empty code"}
	}
	reqBody := CheckRequest{Market: "MX", Language: "es-MX", Flights: []string{"..."}, TokenIdentifierValue: code, SupportsCsvTypeTokenOnly: false, BuyNowScenario: "redeem", ClientContext: struct {
		Client       string `json:"client"`
		DeviceFamily string `json:"deviceFamily"`
	}{Client: "AccountMicrosoftCom", DeviceFamily: "Web"}}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return Result{Code: code, Status: "error", Details: fmt.Sprintf("Marshal err: %v", err)}
	}
	req, err := http.NewRequest("POST", baseURL+"?appId=RedeemNow&context=LookupToken", bytes.NewBuffer(jsonData))
	if err != nil {
		return Result{Code: code, Status: "error", Details: fmt.Sprintf("Req err: %v", err)}
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en")
	if wlidToUse != "" {
		req.Header.Set("Authorization", wlidToUse)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("ms-cv", "wsCJqoR290OCqwUQ."+generateVersionFormat())
	req.Header.Set("Origin", "https://www.microsoft.com")
	req.Header.Set("priority", "u=1, i")
	req.Header.Set("Referer", "https://www.microsoft.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36 Edg/135.0.0.0")
	req.Header.Set("x-authorization-muid", "753B818B2FAC4BD5838DC9A81EA7CE5F")
	req.Header.Set("x-ms-market", "MX")
	req.Header.Set("x-ms-vector-id", "9BA0A7A103DA1DB97463855C3C226D3932CDDB90F48827AFC58DC3377E46FC53")
	resp, err := client.Do(req)
	if err != nil {
		var netErr net.Error
		isTimeout := errors.As(err, &netErr) && netErr.Timeout()
		return Result{Code: code, Status: "retry", Details: fmt.Sprintf("Net err: %v", err), IsTimeout: isTimeout}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{Code: code, Status: "error", Details: fmt.Sprintf("Read err: %v", err)}
	}
	if len(body) == 0 {
		return Result{Code: code, Status: "retry", Details: fmt.Sprintf("Empty resp (%d)", resp.StatusCode)}
	}
	var checkResp CheckResponse
	bodyStr := string(body)
	if resp.StatusCode == 429 {
		return Result{Code: code, Status: "retry", Details: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, bodyStr), IsRateLimited: true}
	}
	if resp.StatusCode >= 500 {
		return Result{Code: code, Status: "retry", Details: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, bodyStr)}
	}
	if resp.StatusCode >= 400 {
		if strings.Contains(bodyStr, "RedeemTokenExpired") {
			return Result{Code: code, Status: "expired", Details: bodyStr}
		}
		if strings.Contains(bodyStr, "RedeemTokenAlreadyRedeemed") {
			return Result{Code: code, Status: "redeemed", Details: bodyStr}
		}
		if strings.Contains(bodyStr, "TokenNotFound") {
			return Result{Code: code, Status: "bad", Details: bodyStr}
		}
		if strings.Contains(bodyStr, "TooManyRequests") {
			return Result{Code: code, Status: "retry", Details: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, bodyStr), IsRateLimited: true}
		}
		return Result{Code: code, Status: "error", Details: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, bodyStr)}
	}
	if err := json.Unmarshal(body, &checkResp); err != nil {
		if strings.Contains(bodyStr, "RedeemTokenExpired") {
			return Result{Code: code, Status: "expired", Details: bodyStr}
		}
		if strings.Contains(bodyStr, "RedeemTokenAlreadyRedeemed") {
			return Result{Code: code, Status: "redeemed", Details: bodyStr}
		}
		if strings.Contains(bodyStr, "TokenNotFound") {
			return Result{Code: code, Status: "bad", Details: bodyStr}
		}
		if strings.Contains(bodyStr, "TooManyRequests") {
			return Result{Code: code, Status: "retry", Details: fmt.Sprintf("Non-JSON (%d): %s", resp.StatusCode, bodyStr), IsRateLimited: true}
		}
		return Result{Code: code, Status: "error", Details: fmt.Sprintf("JSON err (%d): %v\nResp: %s", resp.StatusCode, err, bodyStr)}
	}
	if len(checkResp.Events.Cart) > 0 {
		for _, event := range checkResp.Events.Cart {
			if event.Data.Reason == "TooManyRequests" {
				return Result{Code: code, Status: "retry", Details: bodyStr, IsRateLimited: true}
			}
			switch event.Data.Reason {
			case "TokenNotFound":
				return Result{Code: code, Status: "bad", Details: bodyStr}
			case "RedeemTokenExpired":
				return Result{Code: code, Status: "expired", Details: bodyStr}
			case "RedeemTokenAlreadyRedeemed":
				return Result{Code: code, Status: "redeemed", Details: bodyStr}
			}
			if event.Type == "Error" || event.Data.IsUserError {
				return Result{Code: code, Status: "bad", Details: bodyStr}
			}
		}
	}
	if checkResp.TokenType != "" {
		details := bodyStr
		if len(checkResp.Products) > 0 && checkResp.Products[0].Title != "" {
			details = checkResp.Products[0].Title
		}
		return Result{Code: code, Status: "good", Details: details}
	}
	return Result{Code: code, Status: "bad", Details: fmt.Sprintf("Unknown OK resp (%d): %s", resp.StatusCode, bodyStr)}
}

func generateVersionFormat() string {
	first := rand.Intn(9) + 1
	second := rand.Intn(90) + 10
	third := rand.Intn(9) + 1
	return fmt.Sprintf("%d.%d.%d", first, second, third)
}

func main() {
	rand.Seed(time.Now().UnixNano())

	// Create directories and ensure required files exist
	if err := createDirsAndFiles(); err != nil {
		fmt.Printf("Error preparing directories/files: %v\n", err)
		return
	}

	// --- Load Codes ---
	codesFilePath := filepath.Join(inputDir, codesFile)
	codesBytes, err := os.ReadFile(codesFilePath)
	if err != nil {
		fmt.Printf("Error reading codes file %s: %v\n", codesFilePath, err)
		return
	}
	codeList := []string{}
	for _, line := range bytes.Split(codesBytes, []byte("\n")) {
		trimmed := strings.TrimSpace(string(line))
		if trimmed != "" {
			codeList = append(codeList, trimmed)
		}
	}
	totalCodes := len(codeList)
	if totalCodes == 0 {
		fmt.Println("Error: No valid codes found in", codesFilePath)
		return
	}

	wlidInputFilePath := filepath.Join(inputDir, wlidsFile)
	wlidsBytes, err := os.ReadFile(wlidInputFilePath)
	initialWlidList := []string{}

	if err != nil {
		// Error reading WLID file is only a warning now, as the file existence is guaranteed by createDirsAndFiles
		fmt.Printf("Warning: Error reading WLIDs file %s: %v\n", wlidInputFilePath, err)
		fmt.Println("Proceeding without WLIDs from file.")
	} else {
		lines := bytes.Split(wlidsBytes, []byte("\n"))
		for _, line := range lines {
			trimmed := strings.TrimSpace(string(line))
			if trimmed != "" {
				initialWlidList = append(initialWlidList, trimmed)
			}
		}
	}

	// Initialize the global active list with the full initial list
	wlidListMutex.Lock()
	activeWlidList = make([]string, len(initialWlidList))
	copy(activeWlidList, initialWlidList)
	initialActiveWlidCount := len(activeWlidList) // Store initial count for summary
	wlidListMutex.Unlock()

	fmt.Printf("Loaded %d codes from %s.\n", totalCodes, codesFilePath)
	fmt.Printf("Loaded %d initial WLIDs from %s.\n", initialActiveWlidCount, wlidInputFilePath)
	if initialActiveWlidCount == 0 {
		fmt.Println("Warning: No WLIDs loaded. Requests may be less reliable.")
	}

	reader := bufio.NewReader(os.Stdin)
	var concurrentMode bool
	var maxConcurrent int = 1
	fmt.Print("Run in concurrent mode? (y/n, default n): ")
	input, _ := reader.ReadString('\n')
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "y" || input == "yes" {
		concurrentMode = true
		for {
			fmt.Printf("Enter max concurrent requests (e.g., 10, max %d): ", totalCodes)
			input, _ = reader.ReadString('\n')
			input = strings.TrimSpace(input)
			num, err := strconv.Atoi(input)
			if err == nil && num > 0 && num <= totalCodes {
				maxConcurrent = num
				break
			}
			if err == nil && num > totalCodes {
				fmt.Printf("Setting concurrency to max: %d\n", totalCodes)
				maxConcurrent = totalCodes
				break
			}
			if err == nil && num <= 0 {
				fmt.Println("Min 1.")
			} else {
				fmt.Println("Invalid.")
			}
		}
		fmt.Printf("Running concurrent with %d workers.\n", maxConcurrent)
	} else {
		concurrentMode = false
		fmt.Println("Running sequential.")
	}

	var (
		results        []Result
		processedCount uint64
		wg             sync.WaitGroup
		jobs           chan string
		resultChan     chan Result
	)
	var goodCount, badCount, errorCount, expiredCount, retryCount, redeemedCount int64
	startTime := time.Now()

	if concurrentMode {
		jobs = make(chan string, totalCodes)
		resultChan = make(chan Result, totalCodes)
		fmt.Printf("Starting %d workers...\n", maxConcurrent)
		for w := 0; w < maxConcurrent; w++ {
			wg.Add(1)
			workerClient := &http.Client{Timeout: 45 * time.Second}
			go worker(w+1, &wg, workerClient, jobs, resultChan)
		}
		fmt.Println("Sending jobs...")
		for _, code := range codeList {
			jobs <- code
		}
		close(jobs)
		var collectorWg sync.WaitGroup
		collectorWg.Add(1)
		go func() {
			defer collectorWg.Done()
			for i := 0; i < totalCodes; i++ {
				result := <-resultChan
				results = append(results, result)
				switch result.Status {
				case "good":
					atomic.AddInt64(&goodCount, 1)
				case "bad":
					atomic.AddInt64(&badCount, 1)
				case "expired":
					atomic.AddInt64(&expiredCount, 1)
				case "retry":
					atomic.AddInt64(&retryCount, 1)
				case "redeemed":
					atomic.AddInt64(&redeemedCount, 1)
				default:
					atomic.AddInt64(&errorCount, 1)
				}
				if err := saveResult(result); err != nil {
					fmt.Printf("\nErr save %s: %v\n", result.Code, err)
				}
				atomic.AddUint64(&processedCount, 1)
				currentCount := atomic.LoadUint64(&processedCount)
				wlidListMutex.Lock()
				activeWlidsNow := len(activeWlidList)
				wlidListMutex.Unlock()
				fmt.Printf("\rProcessed %d/%d (%.1f%%) | Active WLIDs: %d ", currentCount, totalCodes, float64(currentCount)/float64(totalCodes)*100, activeWlidsNow)
			}
		}()
		fmt.Println("\nWaiting...")
		wg.Wait()
		close(resultChan)
		collectorWg.Wait()
	} else {
		// ... (Sequential execution) ...
		client := &http.Client{Timeout: 30 * time.Second}
		for i, code := range codeList {
			wlidListMutex.Lock()
			activeWlidsNow := len(activeWlidList)
			wlidListMutex.Unlock()
			fmt.Printf("\rProcessing %d/%d (%.1f%%) | Active WLIDs: %d ", i+1, totalCodes, float64(i+1)/float64(totalCodes)*100, activeWlidsNow)
			selectedWlid := getRandomWlid()
			result := checkCode(client, code, selectedWlid)
			results = append(results, result)
			if result.Status == "retry" && selectedWlid != "" && (result.IsTimeout || result.IsRateLimited) {
				reason := "Timeout"
				if result.IsRateLimited {
					reason = "Rate Limit"
				}
				removeWlidAndLog(selectedWlid, reason)
			}
			if err := saveResult(result); err != nil {
				fmt.Printf("\nErr save %s: %v\n", result.Code, err)
			}
			switch result.Status {
			case "good":
				goodCount++
			case "bad":
				badCount++
			case "expired":
				expiredCount++
			case "retry":
				retryCount++
			case "redeemed":
				redeemedCount++
			default:
				errorCount++
			}
		}
		fmt.Println()
	}

	duration := time.Since(startTime)
	fmt.Printf("\nProcessing finished in %s.\n", duration)

	if err := saveActiveWlids(wlidActiveFinalFilePath); err != nil {
		fmt.Printf("\n[Error] Failed to save final active WLID list: %v\n", err)
	}

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Code", "Status", "Details"})
	table.SetAutoWrapText(false)
	table.SetAutoFormatHeaders(true)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetCenterSeparator("")
	table.SetColumnSeparator(" | ")
	table.SetRowSeparator("")
	table.SetHeaderLine(false)
	table.SetBorder(false)
	table.SetTablePadding("  ")
	table.SetNoWhiteSpace(true)
	for _, res := range results {
		detailsDisplay := res.Details
		maxDetailLen := 80
		if len(detailsDisplay) > maxDetailLen {
			detailsDisplay = detailsDisplay[:maxDetailLen] + "..."
		}
		statusStr := res.Status
		switch res.Status {
		case "good":
			statusStr = color.GreenString(res.Status)
		case "bad":
			statusStr = color.RedString(res.Status)
		case "expired":
			statusStr = color.YellowString(res.Status)
		case "retry":
			reason := ""
			if res.IsTimeout {
				reason = " (Timeout)"
			}
			if res.IsRateLimited {
				reason = " (Rate Limit)"
			}
			statusStr = color.MagentaString(res.Status + reason)
		case "redeemed":
			statusStr = color.CyanString(res.Status)
		case "error":
			statusStr = color.HiRedString(res.Status)
		}
		table.Append([]string{res.Code, statusStr, detailsDisplay})
	}
	table.Render()

	// --- Final Summary ---
	wlidListMutex.Lock()
	finalActiveWlids := len(activeWlidList)
	wlidListMutex.Unlock()
	removedThisRun := initialActiveWlidCount - finalActiveWlids

	fmt.Printf("\n--- Summary ---\n")
	color.Green("Good:       %d", goodCount)
	color.Red("Bad:        %d", badCount)
	color.Yellow("Expired:    %d", expiredCount)
	color.Magenta("Retry:      %d", retryCount)
	color.Cyan("Redeemed:   %d", redeemedCount)
	color.HiRed("Errors:     %d", errorCount)
	fmt.Printf("--------------------\n")
	fmt.Printf("Total Codes: %d\n", totalCodes)
	fmt.Printf("WLIDs Started With: %d\n", initialActiveWlidCount)
	fmt.Printf("WLIDs Removed (This Run): %d (logged to %s)\n", removedThisRun, wlidRemovedFilePath)
	fmt.Printf("WLIDs Active (End of Run): %d (saved to %s)\n", finalActiveWlids, wlidActiveFinalFilePath) // Updated message
	fmt.Printf("--------------------\n")
	fmt.Printf("Duration:   %s\n", duration)
	if concurrentMode && totalCodes > 0 && duration.Seconds() > 0 {
		rate := float64(totalCodes) / duration.Seconds()
		fmt.Printf("Rate:       %.2f codes/sec\n", rate)
	}

	fmt.Printf("\nResults saved to '%s' directory.\n", outputDir)
	fmt.Printf("History of removed WLIDs (timeouts/rate limits) appended to '%s'.\n", wlidRemovedFilePath)
	fmt.Printf("Final list of ACTIVE WLIDs (from this run) saved to '%s'.\n", wlidActiveFinalFilePath) // New message
	fmt.Printf("----> To prepare for the next run, you can manually copy '%s' to '%s'.\n", wlidActiveFinalFilePath, filepath.Join(inputDir, wlidsFile))

}

func worker(_ int, wg *sync.WaitGroup, client *http.Client, jobs <-chan string, results chan<- Result) {
	defer wg.Done()
	for code := range jobs {
		selectedWlid := getRandomWlid()
		result := checkCode(client, code, selectedWlid)
		if result.Status == "retry" && selectedWlid != "" && (result.IsTimeout || result.IsRateLimited) {
			reason := "Timeout"
			if result.IsRateLimited {
				reason = "Rate Limit"
			}
			removeWlidAndLog(selectedWlid, reason)
		}
		results <- result
	}
}
