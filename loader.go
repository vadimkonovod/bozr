package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/xeipuuv/gojsonschema"
)

// SuiteFile describes location of the suite file.
type SuiteFile struct {
	// Path to the file.
	Path string
	// Base directory from file is loaded.
	BaseDir string
	Ext     string
}

// RelDir returns difference between Path and BaseDir.
func (sf SuiteFile) RelDir() string {
	dir, _ := filepath.Rel(sf.BaseDir, filepath.Dir(sf.Path))
	return dir
}

// ToSuite method deserializes suite representation to the object model.
func (sf SuiteFile) ToSuite() *TestSuite {
	if sf.Path == "" {
		return nil
	}

	path := sf.Path
	info, err := os.Lstat(path)

	if err != nil {
		return nil
	}

	if info.IsDir() {
		debug.Print("Ignore dir: " + sf.Path)
		return nil
	}

	content, e := ioutil.ReadFile(path)

	if e != nil {
		fmt.Println("Cannot read file:", path, "Error: ", e.Error())
		return nil
	}

	var testCases []TestCase
	err = json.Unmarshal(content, &testCases)
	if err != nil {
		fmt.Println("Cannot parse file:", path, "Error: ", err.Error())
		return nil
	}

	su := TestSuite{
		Name:  strings.TrimSuffix(info.Name(), sf.Ext),
		Dir:   sf.RelDir(),
		Cases: testCases,
	}

	return &su
}

// SuiteFileIterator is an interface to iterate over a set of suite files
// in a given directory
type SuiteFileIterator interface {
	HasNext() bool
	Next() *SuiteFile
}

// DirSuiteFileIterator iterates over all suite files inside of specified root folder.
type DirSuiteFileIterator struct {
	RootDir  string
	SuiteExt string

	files []SuiteFile
	pos   int
}

func (ds *DirSuiteFileIterator) init() {
	filepath.Walk(ds.RootDir, ds.addSuiteFile)
	debug.Print("Collected: ", len(ds.files))
}

func (ds *DirSuiteFileIterator) addSuiteFile(path string, info os.FileInfo, err error) error {
	if err != nil {
		return nil
	}

	if info.IsDir() {
		return nil
	}

	if !strings.HasSuffix(info.Name(), ds.SuiteExt) {
		debug.Printf("Skip non suite file: %s\n", path)
		return nil
	}

	ds.files = append(ds.files, SuiteFile{
		Path:    path,
		BaseDir: ds.RootDir,
		Ext:     ds.SuiteExt,
	})
	return nil
}

// HasNext returns true in case there is at least one more suite.
func (ds *DirSuiteFileIterator) HasNext() bool {
	return len(ds.files) > ds.pos
}

// Next returns next deserialized suite.
func (ds *DirSuiteFileIterator) Next() *SuiteFile {
	if len(ds.files) <= ds.pos {
		return nil
	}

	file := ds.files[ds.pos]
	ds.pos = ds.pos + 1
	return &file
}

// NewSuiteLoader returns channel of suites that are read from specified folder.
func NewSuiteLoader(rootDir string, suiteExt string) <-chan TestSuite {
	channel := make(chan TestSuite)

	source := &DirSuiteFileIterator{RootDir: rootDir, SuiteExt: suiteExt}
	source.init()

	go func() {
		for source.HasNext() {
			sf := source.Next()
			if sf == nil {
				continue
			}
			suite := sf.ToSuite()
			if suite == nil {
				continue
			}

			channel <- *suite
		}

		close(channel)
	}()

	return channel
}

// ValidateSuites detects syntax errors in all test suites in the root directory.
func ValidateSuites(rootDir string, suiteExt string) error {
	source := &DirSuiteFileIterator{RootDir: rootDir, SuiteExt: suiteExt}
	source.init()

	errs := make([]*SuiteFileError, 0)

	for source.HasNext() {
		sf := source.Next()

		if sf == nil {
			continue
		}

		err := validateSuite(sf.Path)
		if err != nil {
			errs = append(errs, &SuiteFileError{SuiteFile: sf, err: err})
		}
	}

	if len(errs) == 0 {
		return nil
	}

	return SuitesValidationError{errors: errs}
}

type SuiteFileError struct {
	SuiteFile *SuiteFile
	err       error
}

func (e SuiteFileError) Error() string {
	if e.err == nil {
		return ""
	}

	return fmt.Sprintf("%s: %s", e.SuiteFile.Path, e.err.Error())
}

type SuitesValidationError struct {
	errors []*SuiteFileError
}

func (e SuitesValidationError) Error() string {
	msg := make([]string, 0)
	for _, err := range e.errors {
		msg = append(msg, err.Error())
	}

	return strings.Join(msg, "\n")
}

func isSuite(path string) bool {
	schemaLoader := gojsonschema.NewStringLoader(suiteShapeSchema)

	path, _ = filepath.Abs(path)
	documentLoader := gojsonschema.NewReferenceLoader("file:///" + path)

	result, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err != nil {
		return false
	}

	return result.Valid()
}

func validateSuite(path string) error {
	schemaLoader := gojsonschema.NewStringLoader(suiteDetailedSchema)

	path, _ = filepath.Abs(path)
	documentLoader := gojsonschema.NewReferenceLoader("file:///" + path)

	result, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err != nil {
		return err
	}

	if !result.Valid() {
		msg := make([]string, 0)
		for _, desc := range result.Errors() {
			msg = append(msg, fmt.Sprintf("Field: %s, Error: %s", desc.Field(), desc.Description()))
		}

		return errors.New(strings.Join(msg, "\n"))
	}

	return nil
}

// used to detect suite
const suiteShapeSchema = `
{
  "$schema": "http://json-schema.org/draft-04/schema#",
  "type": "array",
  "items": {
    "type": "object",
    "properties": {
      "name": {
        "type": "string"
      },
      "calls": {
        "type": "array"
      }
    },
    "required": [
      "name",
      "calls"
    ]
  }
}
`

// used to validate suite
const suiteDetailedSchema = `
{
  "$schema": "http://json-schema.org/draft-04/schema#",
  "type": "array",
  "items": {
    "type": "object",
    "properties": {
      "name": {
        "type": "string"
      },
      "description": {
        "type": "string"
      },
      "ignore": {
        "type": "string",
        "minLength": 10
      },
      "calls": {
        "type": "array",
        "items": {
          "type": "object",
          "properties": {
            "args": {
              "type": "object",
              "minProperties": 1
            },
            "on": {
              "type": "object",
              "minProperties": 1,
              "properties": {
                "method": {
                  "type": "string",
                  "enum": [
                    "GET",
                    "POST",
                    "PUT",
                    "DELETE",
                    "HEAD",
                    "OPTIONS",
                    "PATCH",
                    "CONNECT",
                    "TRACE"
                  ]
                },
                "url": {
                  "type": "string"
                },
                "headers": {
                  "type": "object",
                  "minProperties": 1
                },
                "params": {
                  "type": "object",
                  "minProperties": 1
                },
                "body": {
                  "oneOf": [
                    {
                      "type": "string"
                    },
                    {
                      "type": "object"
                    }
                  ]
                },
                "bodyFile": {
                  "type": "string"
                }
              },
              "required": [
                "method",
                "url"
              ]
            },
            "expect": {
              "type": "object",
              "minProperties": 1,
              "properties": {
                "statusCode": {
                  "type": "integer"
                },
                "contentType": {
                  "type": "string"
                },
                "headers": {
                  "type": "object",
                  "minProperties": 1
                },
                "body": {
                  "type": "object",
                  "minProperties": 1
                },
                "bodySchemaFile": {
                  "type": "string"
                },
                "bodySchemaURI": {
                  "type": "string"
                },
                "absent": {
                  "type": "array",
                  "minItems": 1
                }
              },
              "additionalProperties": false
            },
            "remember": {
              "type": "object",
              "minProperties": 1,
              "properties": {
                "body": {
                  "type": "object",
                  "minProperties": 1
                },
                "headers": {
                  "type": "object",
                  "minProperties": 1
                }
              },
              "additionalProperties": false
            }
          },
          "required": [
            "on",
            "expect"
          ]
        }
      }
    },
    "required": [
      "calls"
    ]
  }
}
`
