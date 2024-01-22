package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"html/template"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

//go:embed test_report.html.template
var testReportHTMLTemplateStr string

//go:embed test_report.js
var testReportJsCodeStr string

type (
	goTestOutputRow struct {
		Time     string
		TestName string `json:"Test"`
		Action   string
		Package  string
		Elapsed  float64
		Output   string
	}

	testStatus struct {
		TestName           string
		Package            string
		ElapsedTime        float64
		Order              int
		Output             []string
		Passed             bool
		Skipped            bool
		TestFileName       string
		TestFunctionDetail testFunctionFilePos
	}

	templateData struct {
		TestResultGroupIndicatorWidth  string
		TestResultGroupIndicatorHeight string
		TestResults                    []*testGroupData
		NumOfTestPassed                int
		NumOfTestFailed                int
		NumOfTestSkipped               int
		NumOfTests                     int
		TestDuration                   time.Duration
		ReportTitle                    string
		JsCode                         template.JS
		numOfTestsPerGroup             int
		groupTestsByPackage            bool
		sortTestsByName                bool
		OutputFilename                 string
		TestExecutionDate              string
	}

	testGroupData struct {
		FailureIndicator string
		SkippedIndicator string
		Title            string
		TestResults      []*testStatus
	}

	cmdFlags struct {
		titleFlag    string
		sizeFlag     string
		groupSize    int
		groupPackage bool
		nosortFlag   bool
		listFlag     string
		inputFlag    string
		outputFlag   string
		buildTags    string
		verbose      bool
	}

	goListJSONModule struct {
		Path string
		Dir  string
		Main bool
	}

	goListJSON struct {
		Dir         string
		ImportPath  string
		Name        string
		GoFiles     []string
		TestGoFiles []string
		Module      goListJSONModule
	}

	testFunctionFilePos struct {
		Line int
		Col  int
	}

	testFileDetail struct {
		FileName            string
		TestFunctionFilePos testFunctionFilePos
	}

	testFileDetailsByTest    map[string]*testFileDetail
	testFileDetailsByPackage map[string]testFileDetailsByTest
)

func main() {
	rootCmd, _, _ := initRootCommand()
	if err := rootCmd.Execute(); err != nil {
		_, _ = rootCmd.OutOrStderr().Write([]byte(fmt.Sprintf("failed to execute go-test-report: %s", err)))
		os.Exit(1)
	}
}

func initRootCommand() (*cobra.Command, *templateData, *cmdFlags) {
	flags := &cmdFlags{}
	tmplData := &templateData{}
	rootCmd := &cobra.Command{
		Use:  "go-test-report",
		Long: "Captures go test output via stdin or JSON-file and parses it into a single self-contained html file.",
		RunE: func(cmd *cobra.Command, args []string) (e error) {
			startTime := time.Now()
			if err := parseSizeFlag(tmplData, flags); err != nil {
				return fmt.Errorf("failed to parseSizeFlag: %w", err)
			}
			tmplData.numOfTestsPerGroup = flags.groupSize
			tmplData.groupTestsByPackage = flags.groupPackage
			tmplData.sortTestsByName = !flags.nosortFlag
			tmplData.ReportTitle = flags.titleFlag
			tmplData.OutputFilename = flags.outputFlag
			var stdin *os.File
			if flags.inputFlag != "" {
				file, err := os.Open(flags.inputFlag)
				if err != nil {
					return fmt.Errorf("failed to open input file: %w", err)
				}
				stdin = file
			} else {
				if err := checkIfStdinIsPiped(); err != nil {
					return fmt.Errorf("failed to checkIfStdinIsPiped: %w", err)
				}
				stdin = os.Stdin
			}
			stdinScanner := bufio.NewScanner(stdin)
			testReportHTMLTemplateFile, err := os.Create(tmplData.OutputFilename)
			if err != nil {
				return fmt.Errorf("failed to create output file: %w", err)
			}
			reportFileWriter := bufio.NewWriter(testReportHTMLTemplateFile)
			defer func() {
				_ = stdin.Close()
				if err := reportFileWriter.Flush(); err != nil {
					e = err
				}
				if err := testReportHTMLTemplateFile.Close(); err != nil {
					e = err
				}
			}()
			startTestTime := time.Now()
			allPackageNames, allTests, err := readTestDataFromStdIn(stdinScanner, flags, cmd)
			if err != nil {
				return fmt.Errorf("failed to readTestDataFromStdIn: %w", err)
			}
			elapsedTestTime := time.Since(startTestTime)
			// used to the location of test functions in test go files by package and test function name.
			var testFileDetailByPackage testFileDetailsByPackage
			if flags.listFlag != "" {
				testFileDetailByPackage, err = getAllDetails(flags.listFlag)
			} else {
				testFileDetailByPackage, err = getPackageDetails(allPackageNames, flags.buildTags)
			}
			if err != nil {
				return fmt.Errorf("failed to get details: %w", err)
			}
			err = generateReport(tmplData, allTests, testFileDetailByPackage, elapsedTestTime, reportFileWriter)
			if err != nil {
				return fmt.Errorf("failed to get generateReport: %w", err)
			}
			elapsedTime := time.Since(startTime)
			elapsedTimeMsg := []byte(fmt.Sprintf("[go-test-report] finished in %s\n", elapsedTime))
			if _, err := cmd.OutOrStdout().Write(elapsedTimeMsg); err != nil {
				return fmt.Errorf("failed to write elapsedTimeMsg: %w", err)
			}
			return nil
		},
	}
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Prints the version number of go-test-report",
		RunE: func(cmd *cobra.Command, args []string) error {
			msg := fmt.Sprintf("go-test-report v%s", version)
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), msg); err != nil {
				return fmt.Errorf("failed to print: %w", err)
			}
			return nil
		},
	}
	rootCmd.AddCommand(versionCmd)
	rootCmd.PersistentFlags().BoolVarP(&flags.nosortFlag,
		"no-sort",
		"",
		false,
		"don't sort the packages by name in the list")
	rootCmd.PersistentFlags().StringVarP(&flags.titleFlag,
		"title",
		"t",
		"go-test-report",
		"the title text shown in the test report")
	rootCmd.PersistentFlags().StringVarP(&flags.sizeFlag,
		"size",
		"s",
		"24",
		"the size (in pixels) of the clickable indicator for test result groups")
	rootCmd.PersistentFlags().IntVarP(&flags.groupSize,
		"groupSize",
		"g",
		20,
		"the number of tests per test group indicator")
	rootCmd.PersistentFlags().BoolVarP(&flags.groupPackage,
		"groupPackage",
		"p",
		false,
		"group tests by package instead of by count")
	rootCmd.PersistentFlags().StringVarP(&flags.listFlag,
		"list",
		"l",
		"",
		"the JSON module list")
	rootCmd.PersistentFlags().StringVarP(&flags.inputFlag,
		"input",
		"i",
		"",
		"the JSON input file")
	rootCmd.PersistentFlags().StringVarP(&flags.outputFlag,
		"output",
		"o",
		"test_report.html",
		"the HTML output file")
	rootCmd.PersistentFlags().StringVarP(&flags.buildTags,
		"build-tags",
		"b",
		"",
		"the golang build tags (if all tests files contains build tag - this option is mandatory)")
	rootCmd.PersistentFlags().BoolVarP(&flags.verbose,
		"verbose",
		"v",
		false,
		"while processing, show the complete output from go test ")

	return rootCmd, tmplData, flags
}

func readTestDataFromStdIn(stdinScanner *bufio.Scanner, flags *cmdFlags, cmd *cobra.Command) (allPackageNames map[string]struct{}, allTests map[string]*testStatus, e error) {
	allTests = make(map[string]*testStatus)
	allPackageNames = make(map[string]struct{})

	// read from stdin and parse "go test" results
	order := 0
	for stdinScanner.Scan() {
		lineInput := stdinScanner.Bytes()
		if flags.verbose {
			newline := []byte("\n")
			if _, err := cmd.OutOrStdout().Write(append(lineInput, newline[0])); err != nil {
				return nil, nil, fmt.Errorf("failed to print result to stdout: %w", err)
			}
		}
		goTestOutputRow := &goTestOutputRow{}
		if err := json.Unmarshal(lineInput, goTestOutputRow); err != nil {
			return nil, nil, fmt.Errorf("failed to unmarshal test result: %w", err)
		}
		if goTestOutputRow.TestName != "" {
			var status *testStatus
			key := goTestOutputRow.Package + "." + goTestOutputRow.TestName
			if _, exists := allTests[key]; !exists {
				status = &testStatus{
					TestName: goTestOutputRow.TestName,
					Package:  goTestOutputRow.Package,
					Order:    order,
					Output:   []string{},
				}
				allTests[key] = status
				order += 1
			} else {
				status = allTests[key]
			}
			if goTestOutputRow.Action == "pass" || goTestOutputRow.Action == "fail" || goTestOutputRow.Action == "skip" {
				if goTestOutputRow.Action == "pass" {
					status.Passed = true
				}
				if goTestOutputRow.Action == "skip" {
					status.Skipped = true
				}
				status.ElapsedTime = goTestOutputRow.Elapsed
			}
			allPackageNames[goTestOutputRow.Package] = struct{}{}
			if strings.Contains(goTestOutputRow.Output, "--- PASS:") {
				goTestOutputRow.Output = strings.TrimSpace(goTestOutputRow.Output)
			}
			status.Output = append(status.Output, goTestOutputRow.Output)
		}
	}
	return allPackageNames, allTests, nil
}

func getAllDetails(listFile string) (testFileDetailsByPackage, error) {
	testFileDetailByPackage := testFileDetailsByPackage{}
	// #nosec
	f, err := os.Open(listFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open test result file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()
	list := json.NewDecoder(f)
	for list.More() {
		goListJSON := goListJSON{}
		if err := list.Decode(&goListJSON); err != nil {
			return nil, fmt.Errorf("failed to JSON decode: %w", err)
		}
		packageName := goListJSON.ImportPath
		testFileDetailsByTest, err := getFileDetails(&goListJSON)
		if err != nil {
			return nil, fmt.Errorf("failed to get file details: %w", err)
		}
		testFileDetailByPackage[packageName] = testFileDetailsByTest
	}
	return testFileDetailByPackage, nil
}

func getPackageDetails(allPackageNames map[string]struct{}, buildTags string) (testFileDetailsByPackage, error) {
	var testFileDetailByPackage testFileDetailsByPackage
	ctx := context.Background()
	g, ctx := errgroup.WithContext(ctx)
	details := make(chan testFileDetailsByPackage)
	for packageName := range allPackageNames {
		name := packageName
		g.Go(func() error {
			testFileDetailsByTest, err := getTestDetails(name, buildTags)
			if err != nil {
				return fmt.Errorf("failed to get test details: %w", err)
			}
			select {
			case details <- testFileDetailsByPackage{name: testFileDetailsByTest}:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		})
	}

	go func() {
		_ = g.Wait()
		close(details)
	}()

	testFileDetailByPackage = make(testFileDetailsByPackage, len(allPackageNames))
	for d := range details {
		for packageName, testFileDetailsByTest := range d {
			testFileDetailByPackage[packageName] = testFileDetailsByTest
		}
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("failed to parse test results: %w", err)
	}
	return testFileDetailByPackage, nil
}

func getTestDetails(packageName string, buildTags string) (testFileDetailsByTest, error) {
	var out bytes.Buffer
	var cmd *exec.Cmd
	stringReader := strings.NewReader("")
	args := []string{"list", "-json"}
	if buildTags != "" {
		args = append(args, "-tags="+buildTags)
	}
	args = append(args, packageName)
	cmd = exec.Command("go", args...)
	cmd.Stdin = stringReader
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("failed to run go list -json: %w", err)
	}
	goListJSON := &goListJSON{}
	if err := json.Unmarshal(out.Bytes(), goListJSON); err != nil {
		return nil, fmt.Errorf("failed to unmarshal go test list JSON: %w", err)
	}
	return getFileDetails(goListJSON)
}

func getFileDetails(goListJSON *goListJSON) (testFileDetailsByTest, error) {
	testFileDetailByTest := map[string]*testFileDetail{}
	for _, file := range goListJSON.TestGoFiles {
		sourceFilePath := fmt.Sprintf("%s/%s", goListJSON.Dir, file)
		fileSet := token.NewFileSet()
		f, err := parser.ParseFile(fileSet, sourceFilePath, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("failed to parse details: %w", err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				testFileDetail := &testFileDetail{}
				fileSetPos := fileSet.Position(n.Pos())
				folders := strings.Split(fileSetPos.String(), "/")
				fileNameWithPos := folders[len(folders)-1]
				fileDetails := strings.Split(fileNameWithPos, ":")
				lineNum, _ := strconv.Atoi(fileDetails[1])
				colNum, _ := strconv.Atoi(fileDetails[2])
				testFileDetail.FileName = fileDetails[0]
				testFileDetail.TestFunctionFilePos = testFunctionFilePos{
					Line: lineNum,
					Col:  colNum,
				}
				testFileDetailByTest[x.Name.Name] = testFileDetail
			}
			return true
		})
	}
	return testFileDetailByTest, nil
}

type testRef struct {
	key  string
	pkg  string
	name string
	ord  int
}

type byPackage []testRef
type byName []testRef

func (t byPackage) Len() int {
	return len(t)
}

func (t byPackage) Swap(i, j int) {
	t[i], t[j] = t[j], t[i]
}

func (t byPackage) Less(i, j int) bool {
	return t[i].pkg < t[j].pkg
}

func (t byName) Len() int {
	return len(t)
}

func (t byName) Swap(i, j int) {
	t[i], t[j] = t[j], t[i]
}

func (t byName) Less(i, j int) bool {
	return t[i].name < t[j].name
}

func generateReport(tmplData *templateData, allTests map[string]*testStatus, testFileDetailByPackage testFileDetailsByPackage, elapsedTestTime time.Duration, reportFileWriter *bufio.Writer) error {
	// read the html template from the generated embedded asset go file
	tpl := template.New("test_report.html.template")
	tpl, err := tpl.Parse(testReportHTMLTemplateStr)
	if err != nil {
		return fmt.Errorf("failed to parse HTML template: %w", err)
	}

	tmplData.NumOfTestPassed = 0
	tmplData.NumOfTestFailed = 0
	tmplData.NumOfTestSkipped = 0
	// #nosec
	tmplData.JsCode = template.JS(testReportJsCodeStr)
	tgPackage := ""
	tgCounter := 0
	tgID := 0

	// sort the allTests map by test name (this will produce a consistent order when iterating through the map)
	var tests []testRef
	for test, status := range allTests {
		tests = append(tests, testRef{test, status.Package, status.TestName, status.Order})
	}
	// sort the allTests map by input order
	sort.Slice(tests, func(i, j int) bool {
		return tests[i].ord < tests[j].ord
	})
	if tmplData.groupTestsByPackage {
		sort.Stable(byPackage(tests))
		if tmplData.sortTestsByName {
			sort.Stable(byName(tests))
		}
	} else if tmplData.sortTestsByName {
		sort.Sort(byName(tests))
	}
	for _, test := range tests {
		status := allTests[test.key]
		if tmplData.groupTestsByPackage {
			if tgPackage != "" && status.Package != tgPackage {
				tgID++
			}
			tgPackage = status.Package
		}
		if len(tmplData.TestResults) == tgID {
			tmplData.TestResults = append(tmplData.TestResults, &testGroupData{})
		}
		// add file info(name and position; line and col) associated with the test function
		testFileInfo := testFileDetailByPackage[status.Package][status.TestName]
		if testFileInfo != nil {
			status.TestFileName = testFileInfo.FileName
			status.TestFunctionDetail = testFileInfo.TestFunctionFilePos
		}
		if tmplData.groupTestsByPackage {
			tmplData.TestResults[tgID].Title = tgPackage
		}
		tmplData.TestResults[tgID].TestResults = append(tmplData.TestResults[tgID].TestResults, status)
		if !status.Passed {
			if !status.Skipped {
				tmplData.TestResults[tgID].FailureIndicator = "failed"
				tmplData.NumOfTestFailed++
			} else {
				tmplData.TestResults[tgID].SkippedIndicator = "skipped"
				tmplData.NumOfTestSkipped++
			}
		} else {
			tmplData.NumOfTestPassed++
		}
		if !tmplData.groupTestsByPackage {
			tgCounter++
			if tgCounter == tmplData.numOfTestsPerGroup {
				tgCounter = 0
				tgID++
			}
		}
	}
	tmplData.NumOfTests = tmplData.NumOfTestPassed + tmplData.NumOfTestFailed + tmplData.NumOfTestSkipped
	tmplData.TestDuration = elapsedTestTime.Round(time.Millisecond)
	td := time.Now()
	tmplData.TestExecutionDate = fmt.Sprintf("%s %d, %d %02d:%02d:%02d",
		td.Month(), td.Day(), td.Year(), td.Hour(), td.Minute(), td.Second())
	if err := tpl.Execute(reportFileWriter, tmplData); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}
	return nil
}

func parseSizeFlag(tmplData *templateData, flags *cmdFlags) error {
	flags.sizeFlag = strings.ToLower(flags.sizeFlag)
	if !strings.Contains(flags.sizeFlag, "x") {
		val, err := strconv.Atoi(flags.sizeFlag)
		if err != nil {
			return fmt.Errorf("failed to convert sizeFlag to int: %w", err)
		}
		tmplData.TestResultGroupIndicatorWidth = fmt.Sprintf("%dpx", val)
		tmplData.TestResultGroupIndicatorHeight = fmt.Sprintf("%dpx", val)
		return nil
	}
	if strings.Count(flags.sizeFlag, "x") > 1 {
		return errors.New(`malformed size value; only one x is allowed if specifying with and height`)
	}
	a := strings.Split(flags.sizeFlag, "x")
	valW, err := strconv.Atoi(a[0])
	if err != nil {
		return fmt.Errorf("failed to split sizeFlag: %w", err)
	}
	tmplData.TestResultGroupIndicatorWidth = fmt.Sprintf("%dpx", valW)
	valH, err := strconv.Atoi(a[1])
	if err != nil {
		return fmt.Errorf("failed to convert sizeFlag second value to int: %w", err)
	}
	tmplData.TestResultGroupIndicatorHeight = fmt.Sprintf("%dpx", valH)
	return nil
}

func checkIfStdinIsPiped() error {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return fmt.Errorf("failed to get stdin fileInfo: %w", err)
	}
	if (stat.Mode() & os.ModeCharDevice) == 0 {
		return nil
	}
	return errors.New("ERROR: missing ≪ stdin ≫ pipe")
}
