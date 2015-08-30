// Package junos provides automation for Junos (Juniper Networks) devices, as
// well as interaction with Junos Space.
package junos

import (
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/scottdware/go-netconf/netconf"
	"io/ioutil"
	"log"
	"regexp"
	"strings"
)

// All of our RPC calls we use.
var (
	rpcCommand            = "<command format=\"text\">%s</command>"
	rpcCommandXML         = "<command format=\"xml\">%s</command>"
	rpcCommit             = "<commit-configuration/>"
	rpcCommitAt           = "<commit-configuration><at-time>%s</at-time></commit-configuration>"
	rpcCommitCheck        = "<commit-configuration><check/></commit-configuration>"
	rpcCommitConfirm      = "<commit-configuration><confirmed/><confirm-timeout>%d</confirm-timeout></commit-configuration>"
	rpcFactsRE            = "<get-route-engine-information/>"
	rpcFactsChassis       = "<get-chassis-inventory/>"
	rpcConfigFileSet      = "<load-configuration action=\"set\" format=\"text\"><configuration-set>%s</configuration-set></load-configuration>"
	rpcConfigFileText     = "<load-configuration format=\"text\"><configuration-text>%s</configuration-text></load-configuration>"
	rpcConfigFileXML      = "<load-configuration format=\"xml\"><configuration>%s</configuration></load-configuration>"
	rpcConfigURLSet       = "<load-configuration action=\"set\" format=\"text\" url=\"%s\"/>"
	rpcConfigURLText      = "<load-configuration format=\"text\" url=\"%s\"/>"
	rpcConfigURLXML       = "<load-configuration format=\"xml\" url=\"%s\"/>"
	rpcConfigStringSet    = "<load-configuration action=\"set\" format=\"text\"><configuration-set>%s</configuration-set></load-configuration>"
	rpcConfigStringText   = "<load-configuration format=\"text\"><configuration-text>%s</configuration-text></load-configuration>"
	rpcConfigStringXML    = "<load-configuration format=\"xml\"><configuration>%s</configuration></load-configuration>"
	rpcGetRescue          = "<get-rescue-information><format>text</format></get-rescue-information>"
	rpcGetRollback        = "<get-rollback-information><rollback>%d</rollback><format>text</format></get-rollback-information>"
	rpcGetRollbackCompare = "<get-rollback-information><rollback>0</rollback><compare>%d</compare><format>text</format></get-rollback-information>"
	rpcHardware           = "<get-chassis-inventory/>"
	rpcLock               = "<lock><target><candidate/></target></lock>"
	rpcRescueConfig       = "<load-configuration rescue=\"rescue\"/>"
	rpcRescueDelete       = "<request-delete-rescue-configuration/>"
	rpcRescueSave         = "<request-save-rescue-configuration/>"
	rpcRollbackConfig     = "<load-configuration rollback=\"%d\"/>"
	rpcRoute              = "<get-route-engine-information/>"
	rpcSoftware           = "<get-software-information/>"
	rpcUnlock             = "<unlock><target><candidate/></target></unlock>"
	rpcVersion            = "<get-software-information/>"
	rpcReboot             = "<request-reboot/>"
	rpcCommitHistory      = "<get-commit-information/>"
	rpcFileList           = "<file-list><detail/><path>%s</path></file-list>"
)

// Junos contains our session state.
type Junos struct {
	Session        *netconf.Session
	Hostname       string
	RoutingEngines int
	Platform       []RoutingEngine
}

// CommitHistory holds all of the commit entries.
type CommitHistory struct {
	Entries []CommitEntry `xml:"commit-history"`
}

// CommitEntry holds information about each prevous commit.
type CommitEntry struct {
	Sequence  int    `xml:"sequence-number"`
	User      string `xml:"user"`
	Method    string `xml:"client"`
	Timestamp string `xml:"date-time"`
}

// RoutingEngine contains the hardware and software information for each route engine.
type RoutingEngine struct {
	Model   string
	Version string
}

type commandXML struct {
	Config string `xml:",innerxml"`
}

type commitError struct {
	Path    string `xml:"error-path"`
	Element string `xml:"error-info>bad-element"`
	Message string `xml:"error-message"`
}

type commitResults struct {
	XMLName xml.Name      `xml:"commit-results"`
	Errors  []commitError `xml:"rpc-error"`
}

type diffXML struct {
	XMLName xml.Name `xml:"rollback-information"`
	Config  string   `xml:"configuration-information>configuration-output"`
}

type hardwareRouteEngines struct {
	XMLName xml.Name              `xml:"multi-routing-engine-results"`
	RE      []hardwareRouteEngine `xml:"multi-routing-engine-item>chassis-inventory"`
}

type hardwareRouteEngine struct {
	XMLName     xml.Name `xml:"chassis-inventory"`
	Serial      string   `xml:"chassis>serial-number"`
	Description string   `xml:"chassis>description"`
}

type versionRouteEngines struct {
	XMLName xml.Name             `xml:"multi-routing-engine-results"`
	RE      []versionRouteEngine `xml:"multi-routing-engine-item>software-information"`
}

type versionRouteEngine struct {
	XMLName     xml.Name             `xml:"software-information"`
	Hostname    string               `xml:"host-name"`
	Platform    string               `xml:"product-model"`
	PackageInfo []versionPackageInfo `xml:"package-information"`
}

type versionPackageInfo struct {
	XMLName         xml.Name `xml:"package-information"`
	PackageName     []string `xml:"name"`
	SoftwareVersion []string `xml:"comment"`
}

// FileList contains information about all files in a given path.
type FileList struct {
	XMLName xml.Name `xml:"directory-list"`
	Path    string   `xml:"directory>directory-name"`
	Total   string   `xml:"directory>total-files"`
	Files   []File   `xml:"directory>file-information"`
	Error   string   `xml:"output,omitempty"`
}

// File contains information about each individual file on the system. Note that
// "Permissions" and "Date" have sub-items that will better display the information.
type File struct {
	Name        string `xml:"file-name"`
	Permissions struct {
		Text string `xml:"format,attr"`
	} `xml:"file-permissions"`
	Owner string `xml:"file-owner"`
	Group string `xml:"file-group"`
	Size  string `xml:"file-size"`
	Date  struct {
		Text string `xml:"format,attr"`
	} `xml:"file-date"`
}

// Close disconnects our session to the device.
func (j *Junos) Close() {
	j.Session.Transport.Close()
}

// RunCommand executes any operational mode command, such as "show" or "request."
// <format> can be one of "text" or "xml."
func (j *Junos) RunCommand(cmd, format string) (string, error) {
	var command string
	command = fmt.Sprintf(rpcCommand, cmd)
	errMessage := "No output available. Please check the syntax of your command."

	if format == "xml" {
		command = fmt.Sprintf(rpcCommandXML, cmd)
	}

	reply, err := j.Session.Exec(netconf.RawRPC(command))
	if err != nil {
		return errMessage, err
	}

	if reply.Errors != nil {
		for _, m := range reply.Errors {
			return errMessage, errors.New(m.Message)
		}
	}

	if reply.Data == "" {
		return errMessage, nil
	}

	if format == "text" {
		var output commandXML
		err = xml.Unmarshal([]byte(reply.Data), &output)
		if err != nil {
			return "", err
		}

		return output.Config, nil
	}

	return reply.Data, nil
}

// CommitHistory gathers all the information about previous commits.
func (j *Junos) CommitHistory() (*CommitHistory, error) {
	var history CommitHistory
	reply, err := j.Session.Exec(netconf.RawRPC(rpcCommitHistory))
	if err != nil {
		return nil, err
	}

	if reply.Errors != nil {
		for _, m := range reply.Errors {
			return nil, errors.New(m.Message)
		}
	}

	if reply.Data == "" {
		return nil, errors.New("could not load commit history")
	}

	err = xml.Unmarshal([]byte(reply.Data), &history)
	if err != nil {
		return nil, err
	}

	return &history, nil
}

// Commit commits the configuration.
func (j *Junos) Commit() error {
	var errs commitResults
	reply, err := j.Session.Exec(netconf.RawRPC(rpcCommit))
	if err != nil {
		return err
	}

	if reply.Errors != nil {
		for _, m := range reply.Errors {
			return errors.New(m.Message)
		}
	}

	err = xml.Unmarshal([]byte(reply.Data), &errs)
	if err != nil {
		return err
	}

	if errs.Errors != nil {
		for _, m := range errs.Errors {
			message := fmt.Sprintf("[%s]\n    %s\nError: %s", strings.Trim(m.Path, "[\r\n]"), strings.Trim(m.Element, "[\r\n]"), strings.Trim(m.Message, "[\r\n]"))
			return errors.New(message)
		}
	}

	return nil
}

// CommitAt commits the configuration at the specified <time>.
func (j *Junos) CommitAt(time string) error {
	var errs commitResults
	command := fmt.Sprintf(rpcCommitAt, time)
	reply, err := j.Session.Exec(netconf.RawRPC(command))
	if err != nil {
		return err
	}

	if reply.Errors != nil {
		for _, m := range reply.Errors {
			return errors.New(m.Message)
		}
	}

	err = xml.Unmarshal([]byte(reply.Data), &errs)
	if err != nil {
		return err
	}

	if errs.Errors != nil {
		for _, m := range errs.Errors {
			message := fmt.Sprintf("[%s]\n    %s\nError: %s", strings.Trim(m.Path, "[\r\n]"), strings.Trim(m.Element, "[\r\n]"), strings.Trim(m.Message, "[\r\n]"))
			return errors.New(message)
		}
	}

	return nil
}

// CommitCheck checks the configuration for syntax errors.
func (j *Junos) CommitCheck() error {
	var errs commitResults
	reply, err := j.Session.Exec(netconf.RawRPC(rpcCommitCheck))
	if err != nil {
		return err
	}

	if reply.Errors != nil {
		for _, m := range reply.Errors {
			return errors.New(m.Message)
		}
	}

	err = xml.Unmarshal([]byte(reply.Data), &errs)
	if err != nil {
		return err
	}

	if errs.Errors != nil {
		for _, m := range errs.Errors {
			message := fmt.Sprintf("[%s]\n    %s\nError: %s", strings.Trim(m.Path, "[\r\n]"), strings.Trim(m.Element, "[\r\n]"), strings.Trim(m.Message, "[\r\n]"))
			return errors.New(message)
		}
	}

	return nil
}

// CommitConfirm rolls back the configuration after <delay> minutes.
func (j *Junos) CommitConfirm(delay int) error {
	var errs commitResults
	command := fmt.Sprintf(rpcCommitConfirm, delay)
	reply, err := j.Session.Exec(netconf.RawRPC(command))
	if err != nil {
		return err
	}

	if reply.Errors != nil {
		for _, m := range reply.Errors {
			return errors.New(m.Message)
		}
	}

	err = xml.Unmarshal([]byte(reply.Data), &errs)
	if err != nil {
		return err
	}

	if errs.Errors != nil {
		for _, m := range errs.Errors {
			message := fmt.Sprintf("[%s]\n    %s\nError: %s", strings.Trim(m.Path, "[\r\n]"), strings.Trim(m.Element, "[\r\n]"), strings.Trim(m.Message, "[\r\n]"))
			return errors.New(message)
		}
	}

	return nil
}

// ConfigDiff compares the current active configuration to a given rollback configuration.
func (j *Junos) ConfigDiff(compare int) (string, error) {
	var rb diffXML
	command := fmt.Sprintf(rpcGetRollbackCompare, compare)
	reply, err := j.Session.Exec(netconf.RawRPC(command))
	if err != nil {
		return "", err
	}

	if reply.Errors != nil {
		for _, m := range reply.Errors {
			return "", errors.New(m.Message)
		}
	}

	err = xml.Unmarshal([]byte(reply.Data), &rb)
	if err != nil {
		return "", err
	}

	return rb.Config, nil
}

// PrintFacts prints information about the device, such as model and software.
func (j *Junos) PrintFacts() {
	var str string
	fpcRegex := regexp.MustCompile(`^(EX).*`)
	srxRegex := regexp.MustCompile(`^(SRX).*`)
	mRegex := regexp.MustCompile(`^(M[X]?).*`)
	str += fmt.Sprintf("Routing Engines/FPC's: %d\n\n", j.RoutingEngines)
	for i, p := range j.Platform {
		model := p.Model
		version := p.Version
		switch model {
		case fpcRegex.FindString(model):
			str += fmt.Sprintf("fpc%d\n--------------------------------------------------------------------------\n", i)
			str += fmt.Sprintf("Hostname: %s\nModel: %s\nVersion: %s\n\n", j.Hostname, model, version)
		case srxRegex.FindString(model):
			str += fmt.Sprintf("node%d\n--------------------------------------------------------------------------\n", i)
			str += fmt.Sprintf("Hostname: %s\nModel: %s\nVersion: %s\n\n", j.Hostname, model, version)
		case mRegex.FindString(model):
			str += fmt.Sprintf("re%d\n--------------------------------------------------------------------------\n", i)
			str += fmt.Sprintf("Hostname: %s\nModel: %s\nVersion: %s\n\n", j.Hostname, model, version)
		}
	}

	fmt.Println(str)
}

// GetConfig returns the full configuration, or configuration starting at <section>.
// <format> can be one of "text" or "xml." You can do sub-sections by separating the
// <section> path with a ">" symbol, i.e. "system>login" or "protocols>ospf>area".
func (j *Junos) GetConfig(section, format string) (string, error) {
	secs := strings.Split(section, ">")
	nSecs := len(secs) - 1
	command := fmt.Sprintf("<get-configuration format=\"%s\"><configuration>", format)
	if section == "full" {
		command += "</configuration></get-configuration>"
	} else if nSecs >= 0 {
		for i := 0; i < nSecs; i++ {
			command += fmt.Sprintf("<%s>", secs[i])
		}
		command += fmt.Sprintf("<%s/>", secs[nSecs])

		for j := nSecs - 1; j >= 0; j-- {
			command += fmt.Sprintf("</%s>", secs[j])
		}
		command += fmt.Sprint("</configuration></get-configuration>")
	}

	reply, err := j.Session.Exec(netconf.RawRPC(command))
	if err != nil {
		return "", err
	}

	if reply.Errors != nil {
		for _, m := range reply.Errors {
			return "", errors.New(m.Message)
		}
	}

	if format == "text" {
		var output commandXML
		err = xml.Unmarshal([]byte(reply.Data), &output)
		if err != nil {
			return "", err
		}

		return output.Config, nil
	}

	return reply.Data, nil
}

// Config loads a given configuration file from your local machine,
// a remote (FTP or HTTP server) location, or via configuration statements
// from variables (type string or []string) within your script. Format can be one of
// "set" "text" or "xml."
func (j *Junos) Config(path interface{}, format string, commit bool) error {
	var command string
	switch format {
	case "set":
		switch path.(type) {
		case string:
			if strings.Contains(path.(string), "tp://") {
				command = fmt.Sprintf(rpcConfigURLSet, path.(string))
			}

			if _, err := ioutil.ReadFile(path.(string)); err != nil {
				command = fmt.Sprintf(rpcConfigStringSet, path.(string))
			} else {
				data, err := ioutil.ReadFile(path.(string))
				if err != nil {
					return err
				}

				command = fmt.Sprintf(rpcConfigFileSet, string(data))
			}
		case []string:
			command = fmt.Sprintf(rpcConfigStringSet, strings.Join(path.([]string), "\n"))
		}
	case "text":
		switch path.(type) {
		case string:
			if strings.Contains(path.(string), "tp://") {
				command = fmt.Sprintf(rpcConfigURLText, path.(string))
			}

			if _, err := ioutil.ReadFile(path.(string)); err != nil {
				command = fmt.Sprintf(rpcConfigStringText, path.(string))
			} else {
				data, err := ioutil.ReadFile(path.(string))
				if err != nil {
					return err
				}

				command = fmt.Sprintf(rpcConfigFileText, string(data))
			}
		case []string:
			command = fmt.Sprintf(rpcConfigStringText, strings.Join(path.([]string), "\n"))
		}
	case "xml":
		switch path.(type) {
		case string:
			if strings.Contains(path.(string), "tp://") {
				command = fmt.Sprintf(rpcConfigURLXML, path.(string))
			}

			if _, err := ioutil.ReadFile(path.(string)); err != nil {
				command = fmt.Sprintf(rpcConfigStringXML, path.(string))
			} else {
				data, err := ioutil.ReadFile(path.(string))
				if err != nil {
					return err
				}

				command = fmt.Sprintf(rpcConfigFileXML, string(data))
			}
		case []string:
			command = fmt.Sprintf(rpcConfigStringXML, strings.Join(path.([]string), "\n"))
		}
	}

	reply, err := j.Session.Exec(netconf.RawRPC(command))
	if err != nil {
		return err
	}

	if commit {
		err = j.Commit()
		if err != nil {
			return err
		}
	}

	if reply.Errors != nil {
		for _, m := range reply.Errors {
			return errors.New(m.Message)
		}
	}

	return nil
}

// Lock locks the candidate configuration.
func (j *Junos) Lock() error {
	reply, err := j.Session.Exec(netconf.RawRPC(rpcLock))
	if err != nil {
		return err
	}

	if reply.Errors != nil {
		for _, m := range reply.Errors {
			return errors.New(m.Message)
		}
	}

	return nil
}

// NewSession establishes a new connection to a Junos device that we will use
// to run our commands against. NewSession also gathers software information
// about the device.
func NewSession(host, user, password string) (*Junos, error) {
	rex := regexp.MustCompile(`^.*\[(.*)\]`)
	s, err := netconf.DialSSH(host, netconf.SSHConfigPassword(user, password))
	if err != nil {
		log.Fatal(err)
	}

	reply, err := s.Exec(netconf.RawRPC(rpcVersion))
	if err != nil {
		return nil, err
	}

	if reply.Errors != nil {
		for _, m := range reply.Errors {
			return nil, errors.New(m.Message)
		}
	}

	if strings.Contains(reply.Data, "multi-routing-engine-results") {
		var facts versionRouteEngines
		err = xml.Unmarshal([]byte(reply.Data), &facts)
		if err != nil {
			return nil, err
		}

		numRE := len(facts.RE)
		hostname := facts.RE[0].Hostname
		res := make([]RoutingEngine, 0, numRE)

		for i := 0; i < numRE; i++ {
			version := rex.FindStringSubmatch(facts.RE[i].PackageInfo[0].SoftwareVersion[0])
			model := strings.ToUpper(facts.RE[i].Platform)
			res = append(res, RoutingEngine{Model: model, Version: version[1]})
		}

		return &Junos{
			Session:        s,
			Hostname:       hostname,
			RoutingEngines: numRE,
			Platform:       res,
		}, nil
	}

	var facts versionRouteEngine
	err = xml.Unmarshal([]byte(reply.Data), &facts)
	if err != nil {
		return nil, err
	}

	res := make([]RoutingEngine, 0)
	hostname := facts.Hostname
	version := rex.FindStringSubmatch(facts.PackageInfo[0].SoftwareVersion[0])
	model := strings.ToUpper(facts.Platform)
	res = append(res, RoutingEngine{Model: model, Version: version[1]})

	return &Junos{
		Session:        s,
		Hostname:       hostname,
		RoutingEngines: 1,
		Platform:       res,
	}, nil
}

// Rescue will create or delete the rescue configuration given "save" or "delete."
func (j *Junos) Rescue(action string) error {
	command := fmt.Sprintf(rpcRescueSave)

	if action == "delete" {
		command = fmt.Sprintf(rpcRescueDelete)
	}

	reply, err := j.Session.Exec(netconf.RawRPC(command))
	if err != nil {
		return err
	}

	if reply.Errors != nil {
		for _, m := range reply.Errors {
			return errors.New(m.Message)
		}
	}

	return nil
}

// RollbackConfig loads and commits the configuration of a given rollback or rescue state.
func (j *Junos) RollbackConfig(option interface{}) error {
	var command = fmt.Sprintf(rpcRollbackConfig, option)

	if option == "rescue" {
		command = fmt.Sprintf(rpcRescueConfig)
	}

	reply, err := j.Session.Exec(netconf.RawRPC(command))
	if err != nil {
		return err
	}

	err = j.Commit()
	if err != nil {
		return err
	}

	if reply.Errors != nil {
		for _, m := range reply.Errors {
			return errors.New(m.Message)
		}
	}

	return nil
}

// Unlock unlocks the candidate configuration.
func (j *Junos) Unlock() error {
	reply, err := j.Session.Exec(netconf.RawRPC(rpcUnlock))
	if err != nil {
		return err
	}

	if reply.Errors != nil {
		for _, m := range reply.Errors {
			return errors.New(m.Message)
		}
	}

	return nil
}

// Reboot will reboot the device.
func (j *Junos) Reboot() error {
	reply, err := j.Session.Exec(netconf.RawRPC(rpcReboot))
	if err != nil {
		return err
	}

	if reply.Errors != nil {
		for _, m := range reply.Errors {
			return errors.New(m.Message)
		}
	}

	return nil
}

// Files will list all of the file and directory information in the given <path>.
func (j *Junos) Files(path string) (*FileList, error) {
	dir := strings.TrimRight(path, "/")
	var files FileList
	var command = fmt.Sprintf(rpcFileList, dir+"/")
	errMessage := "No output available. Please check the syntax of your command."

	reply, err := j.Session.Exec(netconf.RawRPC(command))
	if err != nil {
		return nil, err
	}

	if reply.Errors != nil {
		for _, m := range reply.Errors {
			return nil, errors.New(m.Message)
		}
	}

	if reply.Data == "" {
		return nil, errors.New(errMessage)
	}

	data := strings.Replace(reply.Data, "\n", "", -1)
	err = xml.Unmarshal([]byte(data), &files)
	if err != nil {
		return nil, err
	}

	if len(files.Error) > 0 {
		return nil, errors.New(fmt.Sprintf("%s: No such file or directory", path))
	}

	return &files, nil
}
