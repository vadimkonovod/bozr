package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	version = "0.8.9"
)

func init() {
	flag.Usage = func() {
		h := "Usage:\n"
		h += "  bozr [OPTIONS] (DIR|FILE)\n\n"

		h += "Options:\n"
		h += "  -d, --debug		Enable debug mode\n"
		h += "  -H, --host		Server to test\n"
		h += "  -w, --worker		Execute in parallel with specified number of workers\n"
		h += "  -h, --help		Print usage\n"
		h += "  -i, --info		Enable info mode. Print request and response details\n"
		h += "      --junit		Enable junit xml reporter\n"
		h += "      --junit-output	Destination for junit report files\n"
		h += "  -v, --version		Print version information and quit\n\n"

		h += "Examples:\n"
		h += "  bozr ./examples\n"
		h += "  bozr -w 2 ./examples\n"
		h += "  bozr -H http://example.com ./examples \n"

		fmt.Fprintf(os.Stderr, h)
	}
}

var (
	suitesDir       string
	hostFlag        string
	routinesFlag    int
	infoFlag        bool
	debugFlag       bool
	helpFlag        bool
	versionFlag     bool
	junitFlag       bool
	junitOutputFlag string

	info  *log.Logger
	debug *log.Logger
)

const suiteExt = ".suite.json"

func initLogger() {
	infoHandler := ioutil.Discard
	debugHandler := ioutil.Discard

	if infoFlag {
		infoHandler = os.Stdout
	}

	if debugFlag {
		debugHandler = os.Stdout
	}

	info = log.New(infoHandler, "", 0)
	debug = log.New(debugHandler, "DEBUG: ", log.Ltime|log.Lshortfile)
}

func main() {
	flag.BoolVar(&debugFlag, "d", false, "Enable debug mode.")
	flag.BoolVar(&debugFlag, "debug", false, "Enable debug mode")

	flag.BoolVar(&infoFlag, "i", false, "Enable info mode. Print request and response details.")
	flag.BoolVar(&infoFlag, "info", false, "Enable info mode. Print request and response details.")

	flag.StringVar(&hostFlag, "H", "", "Test server address. Example: http://example.com/api.")
	flag.IntVar(&routinesFlag, "w", 1, "Execute test sutes in parallel with provided numer of workers. Default is 1.")

	flag.BoolVar(&helpFlag, "h", false, "Print usage")
	flag.BoolVar(&helpFlag, "help", false, "Print usage")

	flag.BoolVar(&versionFlag, "v", false, "Print version information and quit")
	flag.BoolVar(&versionFlag, "version", false, "Print version information and quit")

	flag.BoolVar(&junitFlag, "junit", false, "Enable junit xml reporter")
	flag.StringVar(&junitOutputFlag, "junit-output", "./report", "Destination for junit report files. Default ")

	flag.Parse()

	initLogger()

	if versionFlag {
		fmt.Println("bozr version " + version)
		return
	}

	if helpFlag {
		flag.Usage()
		return
	}

	if len(hostFlag) > 0 {
		_, err := url.ParseRequestURI(hostFlag)
		if err != nil {
			terminate("Invalid host is specified.")
			return
		}
	}

	if routinesFlag < 1 || routinesFlag > 9 {
		fmt.Println("Invalid routines parameter [", routinesFlag, "]. Setting to default [1]")
		routinesFlag = 1
	}

	suitesDir = flag.Arg(0)

	if suitesDir == "" {
		terminate("You must specify a directory or file with tests.")
		flag.Usage()
		return
	}

	// check specified source dir/file exists
	_, err := os.Lstat(suitesDir)
	if err != nil {
		terminate(err.Error())
		return
	}

	err = ValidateSuites(suitesDir, suiteExt)
	if err != nil {
		terminate("One or more test suites are invalid.", err.Error())
		return
	}

	loader := NewSuiteLoader(suitesDir, suiteExt)
	reporter := createReporter()

	RunParallel(loader, reporter, runSuite, routinesFlag)
}

func runSuite(suite TestSuite) []TestResult {
	results := []TestResult{}

	for _, testCase := range suite.Cases {

		result := TestResult{
			Suite:     suite,
			Case:      testCase,
			ExecFrame: TimeFrame{Start: time.Now(), End: time.Now()},
		}

		if testCase.Ignore != nil {
			result.Skipped = true
			result.SkippedMsg = *testCase.Ignore

			results = append(results, result)
			continue
		}

		rememberedMap := make(map[string]interface{})
		for _, c := range testCase.Calls {
			addAll(c.Args, rememberedMap)
			terr := call(suite.Dir, c, rememberedMap)
			if terr != nil {
				result.Error = terr
				break
			}
		}

		result.ExecFrame.End = time.Now()

		results = append(results, result)
	}

	return results
}

func addAll(src, target map[string]interface{}) {
	for key, val := range src {
		target[key] = val
	}
}

func createReporter() Reporter {
	reporters := []Reporter{NewConsoleReporter()}
	if junitFlag {
		path, _ := filepath.Abs(junitOutputFlag)
		reporters = append(reporters, NewJUnitReporter(path))
	}
	reporter := NewMultiReporter(reporters...)
	reporter.Init()

	return reporter
}

func call(suitePath string, call Call, rememberMap map[string]interface{}) *TError {

	terr := &TError{}

	on := call.On

	dat := []byte(on.Body)
	if on.BodyFile != "" {
		uri, err := toAbsPath(suitePath, on.BodyFile)
		if err != nil {
			terr.Cause = err
			return terr
		}

		if d, err := ioutil.ReadFile(uri); err == nil {
			dat = d
		} else {
			terr.Cause = fmt.Errorf("Can't read body file: %s", err.Error())
			return terr
		}
	}

	req, err := populateRequest(on, string(dat), rememberMap)
	if err != nil {
		terr.Cause = err
		return terr
	}

	printRequestInfo(req, dat)

	client := &http.Client{}

	resp, err := client.Do(req)

	if err != nil {
		debug.Print("Error when sending request", err)
		terr.Cause = err
		return terr
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		debug.Print("Error reading response")
		terr.Cause = err
		return terr
	}

	testResp := Response{http: *resp, body: body}
	terr.Resp = testResp

	info.Println(strings.Repeat("-", 50))
	info.Println(testResp.ToString())
	info.Println("")

	exps, err := expectations(call, suitePath)
	if err != nil {
		terr.Cause = err
		return terr
	}

	for _, exp := range exps {
		checkErr := exp.check(&testResp)
		if checkErr != nil {
			terr.Cause = checkErr
			return terr
		}
	}

	err = rememberBody(&testResp, call.Remember.Body, rememberMap)
	debug.Print("Remember: ", rememberMap)
	if err != nil {
		debug.Print("Error remember")
		terr.Cause = err
		return terr
	}

	rememberHeaders(testResp.http.Header, call.Remember.Headers, rememberMap)

	return nil
}

func populateRequest(on On, body string, rememberMap map[string]interface{}) (*http.Request, error) {

	urlStr, err := urlPrefix(populateRememberedVars(on.URL, rememberMap))
	if err != nil {
		return nil, errors.New("Cannot create request. Invalid url: " + on.URL)
	}

	body = populateRememberedVars(body, rememberMap)
	dat := []byte(body)

	req, err := http.NewRequest(on.Method, urlStr, bytes.NewBuffer(dat))
	if err != nil {
		return nil, err
	}

	for key, value := range on.Headers {
		req.Header.Add(key, populateRememberedVars(value, rememberMap))
	}

	q := req.URL.Query()
	for key, value := range on.Params {
		q.Add(key, populateRememberedVars(value, rememberMap))
	}
	req.URL.RawQuery = q.Encode()

	return req, nil
}

func urlPrefix(p string) (string, error) {
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return p, nil
	}

	return concatURL(hostFlag, p)
}

func concatURL(base string, p string) (string, error) {
	baseURL, err := url.ParseRequestURI(base)
	if err != nil {
		return "", err
	}
	return baseURL.Scheme + "://" + baseURL.Host + path.Join(baseURL.Path, p), nil
}

func populateRememberedVars(str string, rememberMap map[string]interface{}) string {
	res := str
	for varName, val := range rememberMap {
		placeholder := "{" + varName + "}"
		res = strings.Replace(res, placeholder, toString(val), -1)
	}
	return res
}

// toString returns value suitable to insert as an argument
// if value if a float where decimal part is zero - convert to int
func toString(rw interface{}) string {
	var sv interface{} = rw
	if fv, ok := rw.(float64); ok {
		_, frac := math.Modf(fv)
		if frac == 0 {
			sv = int(fv)
		}
	}

	return fmt.Sprintf("%v", sv)
}

func expectations(call Call, suitePath string) ([]ResponseExpectation, error) {
	var exps []ResponseExpectation
	if call.Expect.StatusCode != 0 {
		exps = append(exps, StatusCodeExpectation{statusCode: call.Expect.StatusCode})
	}

	if call.Expect.hasSchema() {
		var (
			schemeURI string
			err       error
		)

		if call.Expect.BodySchemaFile != "" {
			schemeURI, err = toAbsPath(suitePath, call.Expect.BodySchemaFile)
			if err != nil {
				return nil, err
			}
			schemeURI = "file:///" + schemeURI
		}

		if call.Expect.BodySchemaURI != "" {
			isHTTP := strings.HasPrefix(call.Expect.BodySchemaURI, "http://")
			isHTTPS := strings.HasPrefix(call.Expect.BodySchemaURI, "https://")
			if !(isHTTP || isHTTPS) {
				schemeURI = hostFlag + call.Expect.BodySchemaURI
			} else {
				schemeURI = call.Expect.BodySchemaURI
			}
		}
		exps = append(exps, BodySchemaExpectation{schemaURI: schemeURI})
	}

	if len(call.Expect.Body) > 0 {
		exps = append(exps, BodyExpectation{pathExpectations: call.Expect.Body})
	}

	if len(call.Expect.Absent) > 0 {
		exps = append(exps, AbsentExpectation{paths: call.Expect.Absent})
	}

	if len(call.Expect.Headers) > 0 {
		for k, v := range call.Expect.Headers {
			exps = append(exps, HeaderExpectation{Name: k, Value: v})
		}
	}

	if call.Expect.ContentType != "" {
		exps = append(exps, ContentTypeExpectation{call.Expect.ContentType})
	}

	// and so on
	return exps, nil
}

func toAbsPath(suitePath string, assetPath string) (string, error) {
	debug.Printf("Building absolute path using: suiteDir: %s, srcDir: %s, assetPath: %s", suitesDir, suitePath, assetPath)
	if filepath.IsAbs(assetPath) {
		// ignore srcDir
		return assetPath, nil
	}

	uri, err := filepath.Abs(filepath.Join(suitesDir, suitePath, assetPath))
	if err != nil {
		return "", errors.New("Invalid file path: " + assetPath)
	}

	return filepath.ToSlash(uri), nil
}

func rememberBody(resp *Response, remember map[string]string, rememberedMap map[string]interface{}) (err error) {

	for varName, pathLine := range remember {
		body, err := resp.Body()
		if err != nil {
			debug.Print("Can't parse response body to Map for [remember]")
			return err
		}

		if rememberVar, err := GetByPath(body, pathLine); err == nil {
			rememberedMap[varName] = rememberVar
		} else {
			strErr := fmt.Sprintf("Remembered value not found, path: %v", pathLine)
			err = errors.New(strErr)
		}
		//fmt.Printf("v: %v\n", getByPath(bodyMap, b...))
	}

	return err
}

func rememberHeaders(header http.Header, remember map[string]string, rememberedMap map[string]interface{}) {
	for valueName, headerName := range remember {
		value := header.Get(headerName)
		if value == "" {
			continue
		}

		rememberedMap[valueName] = value
	}
}

func printRequestInfo(req *http.Request, body []byte) {
	info.Println()
	info.Printf("%s %s %s\n", req.Method, req.URL.String(), req.Proto)

	if len(req.Header) > 0 {
		info.Println()
	}

	for k, v := range req.Header {
		info.Printf("%s: %s", k, strings.Join(v, " "))
	}
	info.Println()

	if len(body) > 0 {
		info.Printf(string(body))
	}
}

func terminate(msgLines ...string) {
	for _, line := range msgLines {
		fmt.Fprintln(os.Stderr, line)
	}

	os.Exit(1)
}
