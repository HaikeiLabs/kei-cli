package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
)

type servicePrincipal struct {
	ID          string `json:"service_principal_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Disabled    bool   `json:"disabled"`
	CreateTime  string `json:"create_time"`
	UpdateTime  string `json:"update_time"`
}

func (p servicePrincipal) shortName() string {
	parts := strings.Split(p.Name, "/")
	return parts[len(parts)-1]
}

type createServicePrincipalRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type updateServicePrincipalRequest struct {
	Description *string `json:"description,omitempty"`
	Disabled    *bool   `json:"disabled,omitempty"`
	UpdateMask  string  `json:"update_mask"`
}

var servicePrincipalsGroup = commandGroup{
	name: "service-principals",
	commands: []groupCommand{
		{name: "list", summary: "List service principals"},
		{name: "get", summary: "Get a service principal"},
		{name: "create", summary: "Create a service principal"},
		{name: "update", summary: "Update a service principal"},
		{name: "delete", summary: "Delete a service principal"},
		{name: "bind", summary: "Bind a service principal to an installation and agent"},
		{name: "unbind", summary: "Remove a service principal binding"},
		{name: "bindings", summary: "List service principal bindings"},
	},
}

func runServicePrincipalsCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	sub, rest, code, handled := servicePrincipalsGroup.dispatch(args, stdout, stderr)
	if handled {
		return code
	}
	switch sub {
	case "list":
		return runServicePrincipalsList(rest, stdout, stderr, client, store)
	case "get":
		return runServicePrincipalsGet(rest, stdout, stderr, client, store)
	case "create":
		return runServicePrincipalsCreate(rest, stdout, stderr, stdin, client, store)
	case "update":
		return runServicePrincipalsUpdate(rest, stdout, stderr, stdin, client, store)
	case "delete":
		return runServicePrincipalsDelete(rest, stdout, stderr, stdin, client, store)
	case "bind":
		return runServicePrincipalBind(rest, stdout, stderr, stdin, client, store)
	case "unbind":
		return runServicePrincipalUnbind(rest, stdout, stderr, stdin, client, store)
	case "bindings":
		return runServicePrincipalBindingsCommand(rest, stdout, stderr, client, store)
	default:
		panic("unexpected subcommand: " + sub)
	}
}

func servicePrincipalsPath(orgID, name string) string {
	path := "/api/v1/organizations/" + url.PathEscape(orgID) + "/service-principals"
	if name != "" {
		path += "/" + url.PathEscape(name)
	}
	return path
}

func runServicePrincipalsList(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("service-principals", "list", "List service principals", stdout, stderr)
	jsonOutput := jsonFlag(lc.fs)
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "service-principals list accepts no positional arguments")
		return 2
	}
	session, ok := openAPISession("service-principals list", stderr, client, store)
	if !ok {
		return 1
	}
	_, payload, err := session.do(http.MethodGet, servicePrincipalsPath(session.orgID, ""), nil, nil)
	if err != nil {
		fmt.Fprintf(stderr, "service-principals list: %v\n", err)
		return 1
	}
	var page struct {
		Principals []servicePrincipal `json:"servicePrincipals"`
	}
	if err := json.Unmarshal(payload, &page); err != nil {
		fmt.Fprintf(stderr, "service-principals list: decode response: %v\n", err)
		return 1
	}
	if *jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(page.Principals)
		return 0
	}
	if len(page.Principals) == 0 {
		fmt.Fprintln(stdout, "No service principals found.")
		return 0
	}
	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tDESCRIPTION\tDISABLED\tCREATED")
	for _, p := range page.Principals {
		fmt.Fprintf(table, "%s\t%s\t%t\t%s\n", p.shortName(), p.Description, p.Disabled, p.CreateTime)
	}
	_ = table.Flush()
	return 0
}

func runServicePrincipalsGet(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("service-principals", "get", "Get a service principal", stdout, stderr).WithArgs("NAME")
	jsonOutput := jsonFlag(lc.fs)
	if helpRequested(args) {
		lc.printUsage(lc.stdout)
		return 0
	}
	pos, err := parsePositionals(lc.fs, args, 1)
	if err != nil {
		fmt.Fprintln(stderr, "service-principals get requires a name")
		return 2
	}
	session, ok := openAPISession("service-principals get", stderr, client, store)
	if !ok {
		return 1
	}
	_, payload, err := session.do(http.MethodGet, servicePrincipalsPath(session.orgID, pos[0]), nil, nil)
	if err != nil {
		fmt.Fprintf(stderr, "service-principals get: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	var p servicePrincipal
	if err := json.Unmarshal(payload, &p); err != nil {
		fmt.Fprintf(stderr, "service-principals get: decode response: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Name:        %s\nDescription: %s\nDisabled:    %t\nCreated:     %s\nUpdated:     %s\n", p.shortName(), p.Description, p.Disabled, p.CreateTime, p.UpdateTime)
	return 0
}

func runServicePrincipalsCreate(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("service-principals", "create", "Create a service principal", stdout, stderr)
	name := lc.fs.String("name", "", "service principal name (required)")
	description := lc.fs.String("description", "", "service principal description")
	jsonOutput := jsonFlag(lc.fs)
	yes := yesFlag(lc.fs)
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "service-principals create accepts no positional arguments")
		return 2
	}
	if !requireFlags(lc.fs, stderr, "name") {
		return 2
	}
	if !confirmWrite(stdin, stderr, *yes, "Create service principal "+*name+"?") {
		return 2
	}
	session, ok := openAPISession("service-principals create", stderr, client, store)
	if !ok {
		return 1
	}
	_, payload, err := session.do(http.MethodPost, servicePrincipalsPath(session.orgID, ""), nil, createServicePrincipalRequest{Name: *name, Description: *description})
	if err != nil {
		fmt.Fprintf(stderr, "service-principals create: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	var p servicePrincipal
	if err := json.Unmarshal(payload, &p); err != nil {
		fmt.Fprintf(stderr, "service-principals create: decode response: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Created service principal %s.\n", p.shortName())
	return 0
}

func runServicePrincipalsUpdate(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("service-principals", "update", "Update a service principal", stdout, stderr).WithArgs("NAME")
	description := lc.fs.String("description", "", "new service principal description")
	disabled := lc.fs.Bool("disabled", false, "disable the service principal")
	enabled := lc.fs.Bool("enabled", false, "enable the service principal")
	jsonOutput := jsonFlag(lc.fs)
	yes := yesFlag(lc.fs)
	if helpRequested(args) {
		lc.printUsage(lc.stdout)
		return 0
	}
	pos, err := parsePositionals(lc.fs, args, 1)
	if err != nil {
		fmt.Fprintln(stderr, "service-principals update requires a name")
		return 2
	}
	set := make(map[string]bool)
	lc.fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if !set["description"] && !set["disabled"] && !set["enabled"] {
		fmt.Fprintln(stderr, "service-principals update requires --description, --disabled, or --enabled")
		return 2
	}
	if *disabled && *enabled {
		fmt.Fprintln(stderr, "--disabled and --enabled cannot be used together")
		return 2
	}
	if !confirmWrite(stdin, stderr, *yes, "Update service principal "+pos[0]+"?") {
		return 2
	}
	session, ok := openAPISession("service-principals update", stderr, client, store)
	if !ok {
		return 1
	}
	body := updateServicePrincipalRequest{}
	var mask []string
	if set["description"] {
		body.Description = description
		mask = append(mask, "description")
	}
	if set["disabled"] || set["enabled"] {
		value := *disabled
		body.Disabled = &value
		mask = append(mask, "disabled")
	}
	body.UpdateMask = strings.Join(mask, ",")
	_, payload, err := session.do(http.MethodPatch, servicePrincipalsPath(session.orgID, pos[0]), nil, body)
	if err != nil {
		fmt.Fprintf(stderr, "service-principals update: %v\n", err)
		return 1
	}
	if *jsonOutput {
		_, _ = stdout.Write(payload)
		return 0
	}
	fmt.Fprintf(stdout, "Updated service principal %s.\n", pos[0])
	return 0
}

func runServicePrincipalsDelete(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("service-principals", "delete", "Delete a service principal", stdout, stderr).WithArgs("NAME")
	yes := yesFlag(lc.fs)
	if helpRequested(args) {
		lc.printUsage(lc.stdout)
		return 0
	}
	pos, err := parsePositionals(lc.fs, args, 1)
	if err != nil {
		fmt.Fprintln(stderr, "service-principals delete requires a name")
		return 2
	}
	if !*yes {
		fmt.Fprintln(stderr, "service-principals delete permanently removes the principal; pass --yes to confirm")
		return 2
	}
	session, ok := openAPISession("service-principals delete", stderr, client, store)
	if !ok {
		return 1
	}
	_, _, err = session.do(http.MethodDelete, servicePrincipalsPath(session.orgID, pos[0]), nil, nil)
	if err != nil {
		fmt.Fprintf(stderr, "service-principals delete: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Service principal %s deleted.\n", pos[0])
	return 0
}

func helpRequested(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}
