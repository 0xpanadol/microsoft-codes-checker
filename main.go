package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"
)

const (
	baseURL   = "https://buynow.production.store-web.dynamics.com/v1.0/Redeem/PrepareRedeem"
	outputDir = "output"
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
	Code    string
	Status  string
	Details string
}

func createOutputDirs() error {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %v", outputDir, err)
	}
	return nil
}

// FileMutex ensures thread-safe file operations
var (
	goodFileMutex     sync.Mutex
	badFileMutex      sync.Mutex
	errorFileMutex    sync.Mutex
	expiredFileMutex  sync.Mutex
	retryFileMutex    sync.Mutex
	redeemedFileMutex sync.Mutex
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

func saveResult(result Result) error {
	var filename string
	var mutex *sync.Mutex

	switch result.Status {
	case "good":
		filename = filepath.Join(outputDir, "good.txt")
		mutex = &goodFileMutex
	case "bad":
		filename = filepath.Join(outputDir, "bad.txt")
		mutex = &badFileMutex
	case "expired":
		filename = filepath.Join(outputDir, "expired.txt")
		mutex = &expiredFileMutex
	case "retry":
		filename = filepath.Join(outputDir, "retry.txt")
		mutex = &retryFileMutex
	case "redeemed":
		filename = filepath.Join(outputDir, "redeemed.txt")
		mutex = &redeemedFileMutex
	default:
		filename = filepath.Join(outputDir, "error.txt")
		mutex = &errorFileMutex
	}

	return appendToFile(filename, result.Code+"\n", mutex)
}

func checkCode(code string, wlids []string) Result {
	// Clean the code by removing any whitespace
	code = strings.TrimSpace(code)

	// Skip empty codes
	if code == "" {
		return Result{
			Code:    code,
			Status:  "error",
			Details: "Empty code",
		}
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	reqBody := CheckRequest{
		Market:   "MX",
		Language: "es-MX",
		Flights: []string{
			"sc_abandonedretry", "sc_addasyncpitelemetry", "sc_adddatapropertyiap", "sc_addfocuslocktosubscriptionmodal",
			"sc_additionalv2locales", "sc_aemparamforimage", "sc_aemrdslocale", "sc_allowbuynowrupay",
			"sc_allowcustompifiltering", "sc_allowelo", "sc_allowfincastlerewardsforsubs", "sc_allowmpesapi",
			"sc_allowparallelorderload", "sc_allowpaypay", "sc_allowpaypayforcheckout", "sc_allowpaysafecard",
			"sc_allowpaysafeforus", "sc_allowrupay", "sc_allowrupayforcheckout", "sc_allowsmdmarkettobeprimarypi",
			"sc_allowupi", "sc_allowupiforbuynow", "sc_allowupiforcheckout", "sc_allowupiqr", "sc_allowupiqrforbuynow",
			"sc_allowupiqrforcheckout", "sc_allowvenmo", "sc_allowvenmoforbuynow", "sc_allowvenmoforcheckout",
			"sc_allowverve", "sc_analyticsforbuynow", "sc_announcementtsenabled", "sc_apperrorboundarytsenabled",
			"sc_askaparentinsufficientbalance", "sc_askaparentssr", "sc_askaparenttsenabled", "sc_asyncpiurlupdate",
			"sc_asyncpurchasefailure", "sc_asyncpurchasefailurexboxcom", "sc_authactionts", "sc_autorenewalconsentnarratorfix",
			"sc_bankchallenge", "sc_bankchallengecheckout", "sc_blockcsvpurchasefrombuynow", "sc_blocklegacyupgrade",
			"sc_buynowfocustrapkeydown", "sc_buynowglobalpiadd", "sc_buynowlistpichanges", "sc_buynowprodigilegalstrings",
			"sc_buynowuipreload", "sc_buynowuiprod", "sc_cartcofincastle", "sc_cartrailexperiment1", "sc_cat2itemsfix",
			"sc_cawarrantytermsv2", "sc_checkoutglobalpiadd", "sc_checkoutitemfontweight", "sc_checkoutredeem",
			"sc_clientdebuginfo", "sc_clienttelemetryforceenabled", "sc_clienttorequestorid", "sc_contactpreferenceactionts",
			"sc_contactpreferenceupdate", "sc_contactpreferenceupdatexboxcom", "sc_conversionblockederror", "sc_cpdeclinedv2",
			"sc_culturemarketinfo", "sc_delayretry", "sc_devicerepairpifilter", "sc_digitallicenseterms",
			"sc_disableupgradetrycheckout", "sc_disallowedpaymentoptionstsenabled", "sc_eligibilityapi", "sc_emptycartexperiment",
			"sc_emptyresultcheck", "sc_enablecartcreationerrorparsing", "sc_enablekakaopay", "sc_errorpageviewfix",
			"sc_errorstringsts", "sc_euomnibusprice", "sc_expandedpurchasespinner", "sc_extendpagetagtooverride",
			"sc_fetchlivepersonfromparentwindow", "sc_fincastlebuynowallowlist", "sc_fincastlebuynowv2strings",
			"sc_fincastlecalculation", "sc_fincastlecallerapplicationidcheck", "sc_fincastleui", "sc_fingerprinttagginglazyload",
			"sc_fixforcalculatingtax", "sc_fixredeemautorenew", "sc_flexsubs", "sc_giftingtelemetryfix", "sc_giftlabelsupdate",
			"sc_giftserversiderendering", "sc_globalhidecssphonenumber", "sc_greenshipping", "sc_handledccemptyresponse",
			"sc_hidegcolinefees", "sc_hidesubscriptionprice", "sc_highresolutionimageforredeem", "sc_hipercard",
			"sc_imagelazyload", "sc_inlineshippingselectorgco", "sc_inlineshippingselectormsa", "sc_inlinetempfix",
			"sc_jarvisconsumerprofile", "sc_jarvisinvalidculture", "sc_klarna", "sc_lineitemactionts", "sc_livepersonlistener",
			"sc_loadingspinner", "sc_lowbardiscountmap", "sc_mapinapppostdata", "sc_marketswithmigratingcssphonenumber",
			"sc_morayfont", "sc_moraystyle", "sc_narratoraddress", "sc_newcheckoutselectorforxboxcom", "sc_newconversionurl",
			"sc_newflexiblepaymentsmessage", "sc_nextpidl", "sc_noawaitforupdateordercall", "sc_officescds",
			"sc_optionalcatalogclienttype", "sc_ordercheckoutfix", "sc_orderpisyncdisabled", "sc_outofstock",
			"sc_passthroughculture", "sc_paymentchallengets", "sc_paymentoptionnotfound", "sc_paymentsessioninsummarypage",
			"sc_pidlignoreesckey", "sc_pitelemetryupdates", "sc_preloadpidlcontainerts", "sc_productforlicenseterms",
			"sc_productimageoptimization", "sc_prominenteddchange", "sc_promocode", "sc_psd2forgco", "sc_purchaseblock",
			"sc_purchaseblockerrorhandling", "sc_purchasedblocked", "sc_purchasedblockedby", "sc_quantitycap", "sc_railv2",
			"sc_reactcheckout", "sc_readytopurchasefix", "sc_recochannelts", "sc_redeemfocusforce", "sc_redeemgotolink",
			"sc_reloadiflineitemdiscrepancy", "sc_removeresellerforstoreapp", "sc_resellerdetail", "sc_returnoospsatocart",
			"sc_rspv2", "sc_scenariotelemetryrefactor", "sc_separatedigitallicenseterms", "sc_setbehaviordefaultvalue",
			"sc_shippingallowlist", "sc_showcontactsupportlink", "sc_showtax", "sc_skippurchaseconfirm", "sc_skipselectpi",
			"sc_splipidltresourcehelper", "sc_surveyurlv2", "sc_taxamountsubjecttochange", "sc_testflight",
			"sc_updateallowedpaymentmethodstoadd", "sc_updatebillinginfo", "sc_updateformatjsx", "sc_updateredemptionlink",
			"sc_updatewarrantycompletesurfaceproinlinelegalterm", "sc_updatewarrantytermslink", "sc_usefullminimaluhf",
			"sc_usehttpsurlstrings", "sc_usekoreanlegaltermstring", "sc_uuid", "sc_xboxcomnosapi", "sc_xboxrecofix",
			"sc_xboxredirection", "sc_xdlshipbuffer",
		},
		TokenIdentifierValue:     code,
		SupportsCsvTypeTokenOnly: false,
		BuyNowScenario:           "redeem",
		ClientContext: struct {
			Client       string `json:"client"`
			DeviceFamily string `json:"deviceFamily"`
		}{
			Client:       "AccountMicrosoftCom",
			DeviceFamily: "Web",
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return Result{
			Code:    code,
			Status:  "error",
			Details: fmt.Sprintf("Error marshaling request: %v", err),
		}
	}

	req, err := http.NewRequest("POST", baseURL+"?appId=RedeemNow&context=LookupToken", bytes.NewBuffer(jsonData))
	if err != nil {
		return Result{
			Code:    code,
			Status:  "error",
			Details: fmt.Sprintf("Error creating request: %v", err),
		}
	}

	// Add headers
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en")
	// Add random WLID
	if len(wlids) > 0 {
		trimmedWlid := strings.TrimSpace(wlids[rand.Intn(len(wlids))])
		req.Header.Set("Authorization", trimmedWlid)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("ms-cv", "wsCJqoR290OCqwUQ."+generateVersionFormat()) // wsCJqoR290OCqwUQ.8.65.3
	req.Header.Set("Origin", "https://www.microsoft.com")
	req.Header.Set("priority", "u=1, i")
	req.Header.Set("Referer", "https://www.microsoft.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36 Edg/135.0.0.0")
	req.Header.Set("x-authorization-muid", "753B818B2FAC4BD5838DC9A81EA7CE5F")
	req.Header.Set("x-ms-market", "MX")
	req.Header.Set("x-ms-vector-id", "9BA0A7A103DA1DB97463855C3C226D3932CDDB90F48827AFC58DC3377E46FC53")

	resp, err := client.Do(req)
	if err != nil {
		return Result{
			Code:    code,
			Status:  "error",
			Details: fmt.Sprintf("Error making request: %v", err),
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{
			Code:    code,
			Status:  "error",
			Details: fmt.Sprintf("Error reading response: %v", err),
		}
	}

	// Check if response is empty
	if len(body) == 0 {
		return Result{
			Code:    code,
			Status:  "error",
			Details: fmt.Sprintf("Empty response from server (Status: %d)", resp.StatusCode),
		}
	}

	// Try to parse the response
	var checkResp CheckResponse
	if err := json.Unmarshal(body, &checkResp); err != nil {
		// If we can't parse the JSON, check if it contains error indicators
		bodyStr := string(body)

		// Check for specific error conditions
		if strings.Contains(bodyStr, "RedeemTokenExpired") {
			return Result{
				Code:    code,
				Status:  "expired",
				Details: bodyStr,
			}
		}

		// If it's a network error or other issue
		if resp.StatusCode != 200 {
			return Result{
				Code:    code,
				Status:  "error",
				Details: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, bodyStr),
			}
		}

		return Result{
			Code:    code,
			Status:  "error",
			Details: fmt.Sprintf("Error parsing response: %v\nResponse: %s", err, bodyStr),
		}
	}

	// Check for specific error conditions in the parsed response
	if len(checkResp.Events.Cart) > 0 {
		for _, event := range checkResp.Events.Cart {
			switch event.Data.Reason {
			case "TokenNotFound":
				return Result{
					Code:    code,
					Status:  "bad",
					Details: string(body),
				}
			case "TooManyRequests":
				return Result{
					Code:    code,
					Status:  "retry",
					Details: string(body),
				}
			case "RedeemTokenExpired":
				return Result{
					Code:    code,
					Status:  "expired",
					Details: string(body),
				}
			case "RedeemTokenAlreadyRedeemed":
				return Result{
					Code:    code,
					Status:  "redeemed",
					Details: string(body),
				}
			}
		}
	}

	// Determine status based on response
	if checkResp.TokenType != "" {
		return Result{
			Code:    code,
			Status:  "good",
			Details: string(body),
		}
	} else if len(checkResp.Events.Cart) > 0 {
		return Result{
			Code:    code,
			Status:  "bad",
			Details: string(body),
		}
	}

	return Result{
		Code:    code,
		Status:  "error",
		Details: string(body),
	}
}

func main() {
	// Seed the random number generator
	rand.Seed(time.Now().UnixNano())

	if err := createOutputDirs(); err != nil {
		fmt.Printf("Error creating output directories: %v\n", err)
		return
	}

	// Create input directory if it doesn't exist
	if err := os.MkdirAll("input", 0755); err != nil {
		fmt.Printf("Error creating input directory: %v\n", err)
		return
	}

	// Check if input files exist
	if _, err := os.Stat("input/codes.txt"); os.IsNotExist(err) {
		fmt.Println("Error: input/codes.txt file not found")
		fmt.Println("Please create the file with one code per line")
		return
	}

	if _, err := os.Stat("input/wlids.txt"); os.IsNotExist(err) {
		fmt.Println("Error: input/wlids.txt file not found")
		fmt.Println("Please create the file with one WLID per line")
		return
	}

	// Read codes from file
	codes, err := os.ReadFile("input/codes.txt")
	if err != nil {
		fmt.Printf("Error reading codes file: %v\n", err)
		return
	}

	// Read WLIDs from file
	wlids, err := os.ReadFile("input/wlids.txt")
	if err != nil {
		fmt.Printf("Error reading WLIDs file: %v\n", err)
		return
	}

	// Split into lines and convert to strings
	codeList := bytes.Split(bytes.TrimSpace(codes), []byte("\n"))
	wlidList := bytes.Split(bytes.TrimSpace(wlids), []byte("\n"))

	// Check if we have any codes to process
	if len(codeList) == 0 {
		fmt.Println("Error: No codes found in input/codes.txt")
		return
	}

	// Check if we have any WLIDs
	if len(wlidList) == 0 {
		fmt.Println("Warning: No WLIDs found in input/wlids.txt. Requests may fail.")
	}

	// Convert WLID list to strings
	wlidStrings := make([]string, len(wlidList))
	for i, wlid := range wlidList {
		wlidStrings[i] = string(wlid)
	}

	// Create table writer
	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Code", "Status", "Details"})
	table.SetAutoWrapText(false)
	table.SetAutoFormatHeaders(true)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetCenterSeparator("")
	table.SetColumnSeparator("")
	table.SetRowSeparator("")
	table.SetHeaderLine(false)
	table.SetBorder(false)
	table.SetTablePadding("\t")
	table.SetNoWhiteSpace(true)

	// Process results
	goodCount := 0
	badCount := 0
	errorCount := 0
	expiredCount := 0
	retryCount := 0
	redeemedCount := 0

	// Process codes sequentially
	totalCodes := len(codeList)
	for i, code := range codeList {
		// Print progress
		fmt.Printf("\rProcessing code %d/%d (%.1f%%)", i+1, totalCodes, float64(i+1)/float64(totalCodes)*100)

		// Check code
		result := checkCode(string(code), wlidStrings)

		// Save result to file
		if err := saveResult(result); err != nil {
			fmt.Printf("\nError saving result for code %s: %v\n", result.Code, err)
			continue
		}

		// Update counters
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

		// Add to table
		table.Append([]string{
			result.Code,
			result.Status,
			fmt.Sprintf("%.100s...", result.Details),
		})

		// Dodge the rate limit
		time.Sleep(2 * time.Second)
	}

	// Print newline after progress
	fmt.Println()

	// Print results
	table.Render()

	// Print summary
	fmt.Printf("\nSummary:\n")
	color.Green("Good codes: %d", goodCount)
	color.Red("Bad codes: %d", badCount)
	color.Yellow("Expired codes: %d", expiredCount)
	color.Magenta("Retry codes: %d", retryCount)
	color.Cyan("Redeemed codes: %d", redeemedCount)
	color.Yellow("Errors: %d", errorCount)
}

func generateVersionFormat() string {
	rand.Seed(time.Now().UnixNano())

	first := rand.Intn(9) + 1    // 1-9
	second := rand.Intn(90) + 10 // 10-99
	third := rand.Intn(9) + 1    // 1-9

	return fmt.Sprintf("%d.%d.%d", first, second, third)
}
