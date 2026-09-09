package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// "showmeshctl show participation": which FPP instances and which Resolume
// instances take part in a show. Participation is SELECTED by an operator,
// never derived from what happens to be connected or from what a playlist
// mentions, so this verb is the whole of the coordinator's knowledge on
// the subject.
//
// Three states, and this file exists mostly to keep them apart at the
// command line: a selection that was never recorded, a selection that is
// deliberately empty, and a populated one. Both this verb and "show set"
// carry an unnamed integration forward rather than clearing it, so
// "leave it alone" and "record that none take part" can never be confused
// for each other. This verb is the one that leaves the show's name and
// notes alone too.

// participationFlags is the flag group both "show set" and
// "show participation set" register. Both carry an unnamed integration
// forward, so both offer withUnset: with silence meaning "leave it
// alone", removing a selection needs a flag that says so, and it is a
// different instruction from --fpp-none.
type participationFlags struct {
	fpp, resolume           string
	fppSet, resolumeSet     bool
	fppNone, resolumeNone   bool
	fppUnset, resolumeUnset bool
	withUnset               bool
}

func registerParticipationFlags(fs *flag.FlagSet, withUnset bool) *participationFlags {
	p := &participationFlags{withUnset: withUnset}
	fs.Func("fpp", "comma-separated FPP instance ids that take part in this show", func(v string) error {
		p.fpp, p.fppSet = v, true
		return nil
	})
	fs.BoolVar(&p.fppNone, "fpp-none", false,
		"record that NO FPP instance takes part in this show (an explicit empty selection, not an absent one)")
	fs.Func("resolume", "comma-separated Resolume instance ids that take part in this show", func(v string) error {
		p.resolume, p.resolumeSet = v, true
		return nil
	})
	fs.BoolVar(&p.resolumeNone, "resolume-none", false,
		"record that NO Resolume instance takes part in this show (an explicit empty selection, not an absent one)")
	if withUnset {
		fs.BoolVar(&p.fppUnset, "fpp-unset", false,
			"remove the FPP selection entirely, returning this show to \"never configured\"")
		fs.BoolVar(&p.resolumeUnset, "resolume-unset", false,
			"remove the Resolume selection entirely, returning this show to \"never configured\"")
	}
	return p
}

// resolve turns the flags into the two values to send, given whatever the
// show currently holds. An integration nobody named keeps currentFPP /
// currentResolume verbatim, which is how a selection survives a write
// that was only meant to rename a show. Pass nil for a show that does not
// exist yet.
func (p *participationFlags) resolve(currentFPP, currentResolume *[]string) (fpp, resolume *[]string, err error) {
	fpp, err = resolveOneSelection("fpp", p.fppSet, p.fpp, p.fppNone, p.fppUnset, currentFPP)
	if err != nil {
		return nil, nil, err
	}
	resolume, err = resolveOneSelection("resolume", p.resolumeSet, p.resolume, p.resolumeNone, p.resolumeUnset, currentResolume)
	if err != nil {
		return nil, nil, err
	}
	return fpp, resolume, nil
}

// resolveOneSelection settles one integration. Naming it two ways at once
// is refused: ranking the flags would mean this program deciding which of
// two contradictory instructions the operator meant.
func resolveOneSelection(name string, listSet bool, list string, none, unset bool, current *[]string) (*[]string, error) {
	given := []string{}
	if listSet {
		given = append(given, "--"+name)
	}
	if none {
		given = append(given, "--"+name+"-none")
	}
	if unset {
		given = append(given, "--"+name+"-unset")
	}
	if len(given) > 1 {
		return nil, fmt.Errorf("%s contradict each other; give one", strings.Join(given, " and "))
	}
	switch {
	case listSet:
		ids := parseInstanceList(list)
		if len(ids) == 0 {
			return nil, fmt.Errorf("--%s was given no instance id; use --%s-none to record that none take part", name, name)
		}
		return &ids, nil
	case none:
		empty := []string{}
		return &empty, nil
	case unset:
		return nil, nil
	default:
		return current, nil
	}
}

// validate reports a contradictory or empty flag combination without
// sending anything. Called before the client is built so a mistyped
// command is refused locally rather than after a round trip.
func (p *participationFlags) validate() error {
	_, _, err := p.resolve(nil, nil)
	return err
}

// anyGiven reports whether the operator named any integration at all.
func (p *participationFlags) anyGiven() bool {
	return p.fppSet || p.fppNone || p.fppUnset || p.resolumeSet || p.resolumeNone || p.resolumeUnset
}

// parseInstanceList splits a comma-separated flag value into ids. It does
// not check the id syntax or reject a repeat: the coordinator is the
// authority on both and names its own refusal, and a second opinion here
// could only ever disagree with it.
func parseInstanceList(csv string) []string {
	out := []string{}
	for _, part := range strings.Split(csv, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// printParticipationLine states all three states out loud, including the
// absent one. An operator reading a show has to be able to see "nobody has
// chosen yet" rather than infer it from a blank line.
func printParticipationLine(w io.Writer, label string, selection *[]string) {
	switch {
	case selection == nil:
		_, _ = fmt.Fprintf(w, "%-10s (no selection recorded)\n", label+":")
	case len(*selection) == 0:
		_, _ = fmt.Fprintf(w, "%-10s (none take part)\n", label+":")
	default:
		_, _ = fmt.Fprintf(w, "%-10s %s\n", label+":", strings.Join(*selection, ", "))
	}
}

func cmdShowParticipation(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printShowParticipationUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printShowParticipationUsage(stdout)
		return exitOK
	case "get":
		return cmdShowParticipationGet(rest, stdout, stderr, clock)
	case "set":
		return cmdShowParticipationSet(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl show participation: unknown subcommand %q\n\n", sub)
		printShowParticipationUsage(stderr)
		return exitUsage
	}
}

func printShowParticipationUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl show participation <get|set> [flags] <show-id>

Read or change which FPP instances and which Resolume instances take part
in a show. Participation is selected by hand: nothing is inferred from
what is connected, reachable, or mentioned in a playlist.

Each integration is in one of three states, and they are not
interchangeable:

  no selection recorded   nobody has said which instances take part; a
                          show created before this setting existed reads
                          this way, and it is not the same as choosing
                          that none take part
  none take part          the operator chose that this integration is not
                          in this show tonight
  a list of ids           the selection

Subcommands:
  get <id>   print the current selection for one show (read)
  set <id>   change the selection (write, requires config:write)

"set" reads the show first and rewrites only what you name: the show's
name, notes, and any integration you leave unnamed are carried forward
unchanged. Use "show set" instead when you want a full replacement.
`)
}

func cmdShowParticipationGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl show participation get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl show participation get [flags] <show-id>")
		_, _ = fmt.Fprintln(stderr, "\nPrint which FPP and which Resolume instances take part in this show")
		_, _ = fmt.Fprintln(stderr, "(GET /api/v1/config/show/{id}).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "show participation get", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "show participation get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp showConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/show/"+url.PathEscape(rest[0]), nil, &resp); err != nil {
		return reportError(stderr, "show participation get", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "show participation get", err)
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "Show ID:   %s\n", resp.ID)
	_, _ = fmt.Fprintf(stdout, "Name:      %s\n", resp.Payload.Name)
	printParticipationLine(stdout, "FPP", resp.Payload.FPPInstances)
	printParticipationLine(stdout, "Resolume", resp.Payload.ResolumeInstances)
	_, _ = fmt.Fprintf(stdout, "Revision:  %d\n", resp.Revision)
	return exitOK
}

func cmdShowParticipationSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl show participation set", stderr)
	participation := registerParticipationFlags(fs, true)
	ifMatchFlag, forceFlag := registerIfMatchFlags(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl show participation set [flags] <show-id>")
		_, _ = fmt.Fprintln(stderr, "\nChange which instances take part in this show (PUT")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/show/{id}). Requires config:write, admin only.")
		_, _ = fmt.Fprintln(stderr, "\nThis command reads the show first and carries forward its name, its")
		_, _ = fmt.Fprintln(stderr, "notes, and any integration you do not name. Naming an integration two")
		_, _ = fmt.Fprintln(stderr, "ways at once (--fpp with --fpp-none, say) is refused rather than ranked.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "show participation set", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]
	if !participation.anyGiven() {
		_, _ = fmt.Fprintln(stderr, "showmeshctl show participation set: name at least one of --fpp, --fpp-none,")
		_, _ = fmt.Fprintln(stderr, "--fpp-unset, --resolume, --resolume-none, --resolume-unset")
		return exitUsage
	}
	if err := participation.validate(); err != nil {
		_, _ = fmt.Fprintf(stderr, "showmeshctl show participation set: %v\n", err)
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "show participation set", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	apiPath := "/api/v1/config/show/" + url.PathEscape(id)
	var current showConfigResponse
	if err := c.getJSON(ctx, apiPath, nil, &current); err != nil {
		return reportError(stderr, "show participation set", err)
	}

	fppSel, resolumeSel, selErr := participation.resolve(current.Payload.FPPInstances, current.Payload.ResolumeInstances)
	if selErr != nil {
		_, _ = fmt.Fprintf(stderr, "showmeshctl show participation set: %v\n", selErr)
		return exitUsage
	}

	ifMatchRevision, ifMatchSet := ifMatchFlag()
	ifMatch, err := resolveIfMatch(forceFlag(), ifMatchRevision, ifMatchSet, current.Revision, func() (int64, error) {
		return current.Revision, nil
	})
	if err != nil {
		return reportError(stderr, "show participation set", err)
	}

	body := configShow{
		Name: current.Payload.Name, Notes: current.Payload.Notes,
		FPPInstances: fppSel, ResolumeInstances: resolumeSel,
	}
	var resp showConfigResponse
	if err := c.putJSON(ctx, apiPath, ifMatch, body, &resp); err != nil {
		return reportError(stderr, "show participation set", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "show participation set", err)
		}
		return exitOK
	}
	printShowDetail(stdout, resp)
	return exitOK
}
