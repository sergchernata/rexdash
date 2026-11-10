// Package cli parses rexdash's arguments.
package cli

import "strconv"

// Args are the parsed command line.
type Args struct {
	Cmd      string
	Pos      []string
	Interval int // seconds; 0 = the pane's default
	Repo     string
	Since    string
	Hide     []string
	Stream   string
}

// Parse reads arguments (without the program name).
func Parse(argv []string) Args {
	a := Args{Since: "15m", Pos: []string{}, Hide: []string{}}
	if len(argv) == 0 {
		return a
	}
	a.Cmd = argv[0]
	next := func(i *int) (string, bool) {
		if *i+1 < len(argv) {
			*i++
			return argv[*i], true
		}
		return "", false
	}
	for i := 1; i < len(argv); i++ {
		switch argv[i] {
		case "-i", "--interval":
			if v, ok := next(&i); ok {
				n, err := strconv.Atoi(v)
				if err == nil && n > 0 {
					a.Interval = n
				} else {
					a.Interval = 0
				}
			}
		case "-r", "--repo":
			a.Repo, _ = next(&i)
		case "--since":
			if v, ok := next(&i); ok {
				a.Since = v
			}
		case "--hide":
			if v, ok := next(&i); ok {
				a.Hide = append(a.Hide, v)
			}
		case "--stream":
			a.Stream, _ = next(&i)
		default:
			a.Pos = append(a.Pos, argv[i])
		}
	}
	return a
}

// awsCommands are the panes that need the AWS SDK; they live in rexdash-aws.
var awsCommands = map[string]bool{"pipeline": true, "infra": true, "fleet": true, "billing": true, "terraform": true, "logs": true}

// IsAWS reports whether cmd is an AWS pane.
func IsAWS(cmd string) bool { return awsCommands[cmd] }
