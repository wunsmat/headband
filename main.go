package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var port = "7777"

var (
	accent = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	dim    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	bad    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	mine   = lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Bold(true)
	win    = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("10")).Bold(true)
	box    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
)

type lostMsg struct{}

type model struct {
	addr   string
	enc    *json.Encoder
	in     chan Update
	state  State
	you    int
	joined bool
	err    string
	pubIP  string

	entry     textinput.Model
	notes     textarea.Model
	onNotes   bool
	wasActive bool
	w, h      int
}

func newModel(addr string) model {
	e := textinput.New()
	e.Placeholder = "your name"
	e.Focus()
	n := textarea.New()
	n.Placeholder = "questions you've asked, what you've ruled out…"
	return model{addr: addr, entry: e, notes: n, wasActive: true}
}

func (m model) Init() tea.Cmd {
	if m.addr != "" {
		return textinput.Blink
	}
	return tea.Batch(textinput.Blink, publicIP)
}

type publicIPMsg string

func publicIP() tea.Msg {
	c := http.Client{Timeout: 5 * time.Second}
	r, err := c.Get("https://api.ipify.org")
	if err != nil {
		return publicIPMsg("")
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 64))
	return publicIPMsg(strings.TrimSpace(string(b)))
}

func waitFor(ch chan Update) tea.Cmd {
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return lostMsg{}
		}
		return u
	}
}

func (m *model) connect(name string) error {
	target := m.addr
	if target == "" {
		if _, err := serve(":" + port); err != nil {
			return err
		}
		target = "127.0.0.1:" + port
	}
	c, err := net.Dial("tcp", target)
	if err != nil {
		return err
	}
	m.enc = json.NewEncoder(c)
	m.in = make(chan Update, 8)
	go func() {
		dec := json.NewDecoder(c)
		for {
			var u Update
			if dec.Decode(&u) != nil {
				close(m.in)
				return
			}
			m.in <- u
		}
	}()
	return m.enc.Encode(Cmd{Cmd: "join", Name: name})
}

func (m *model) setFocus(notes bool) {
	m.onNotes = notes
	if notes {
		m.entry.Blur()
		m.notes.Focus()
	} else {
		m.notes.Blur()
		m.entry.Focus()
	}
}

func (m *model) send(c Cmd) {
	if m.enc != nil && m.enc.Encode(c) != nil {
		m.err = "connection lost"
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case Update:
		m.state, m.you, m.joined = msg.State, msg.You, true
		if a := m.active(); a != m.wasActive {
			m.wasActive = a
			m.setFocus(!a)
		}
		return m, waitFor(m.in)

	case publicIPMsg:
		m.pubIP = string(msg)
		return m, nil

	case lostMsg:
		m.err = "connection lost"
		return m, nil

	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.entry.Width = m.w*3/5 - 8
		m.notes.SetWidth(m.w*2/5 - 4)
		m.notes.SetHeight(m.h - 4)
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "tab":
			if m.active() {
				m.setFocus(!m.onNotes)
			}
			return m, nil
		case "ctrl+s":
			m.send(Cmd{Cmd: "start"})
			return m, nil
		case "ctrl+r":
			m.send(Cmd{Cmd: "restart"})
			return m, nil
		case "ctrl+k":
			m.send(Cmd{Cmd: "skip"})
			return m, nil
		case "enter":
			if !m.onNotes {
				return m.submit()
			}
		}
	}

	var cmd tea.Cmd
	if m.onNotes {
		m.notes, cmd = m.notes.Update(msg)
	} else {
		m.entry, cmd = m.entry.Update(msg)
	}
	return m, cmd
}

func (m model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.entry.Value())
	if text == "" {
		return m, nil
	}
	m.entry.SetValue("")
	if !m.joined {
		if err := m.connect(text); err != nil {
			m.err = err.Error()
			return m, nil
		}
		return m, waitFor(m.in)
	}
	switch m.state.Phase {
	case "assign":
		m.send(Cmd{Cmd: "thing", Text: text})
	case "play":
		m.send(Cmd{Cmd: "guess", Text: text})
	}
	return m, nil
}

func (m model) active() bool {
	if !m.joined {
		return true
	}
	switch m.state.Phase {
	case "assign":
		return m.target().Thing == ""
	case "play":
		return m.state.Turn == m.you
	}
	return false
}

func (m model) target() Player {
	if len(m.state.Assigns) != len(m.state.Players) || m.state.Assigns[m.you] < 0 {
		return Player{}
	}
	return m.state.Players[m.state.Assigns[m.you]]
}

func (m model) status() string {
	if !m.joined {
		return accent.Render("Type your name and hit enter.")
	}
	p := m.state.Players
	switch m.state.Phase {
	case "lobby":
		lines := []string{accent.Render(fmt.Sprintf("Lobby — %d connected.", len(p)))}
		if m.addr == "" {
			lines = append(lines, "Friends run  "+accent.Render("headband "+localIP()+":"+port)+dim.Render("   same house/wifi"))
			if m.pubIP != "" {
				lines = append(lines, "          or "+accent.Render("headband "+m.pubIP+":"+port)+dim.Render("   over the internet — needs a forwarded port or tailscale"))
			}
		} else {
			lines = append(lines, dim.Render("Joined "+m.addr+"."))
		}
		if m.you == 0 {
			return strings.Join(append(lines, accent.Render("ctrl+s to start (2+ players).")), "\n")
		}
		return strings.Join(append(lines, dim.Render("Waiting for the host to start.")), "\n")
	case "assign":
		if m.target().Thing != "" {
			return dim.Render("Sent. Waiting for everyone else to assign.")
		}
		return accent.Render("You assign " + m.target().Name + ". What are they?")
	case "play":
		if p[m.you].Done {
			return win.Render(" YOU GOT IT: "+p[m.you].Thing+" ") +
				dim.Render("\nHang around and watch the others suffer.")
		}
		if m.state.Turn == m.you {
			return accent.Render("YOUR TURN") +
				" — ask your yes/no question on Discord, then type a guess and hit\nenter, or ctrl+k to pass."
		}
		return dim.Render(p[m.state.Turn].Name + " is asking.")
	default:
		return accent.Render("Game over. You were: " + p[m.you].Thing)
	}
}

func (m model) roster() string {
	var b strings.Builder
	for i, q := range m.state.Players {
		thing := q.Thing
		if i == m.you && !q.Done {
			thing = "???"
		} else if thing == "" {
			thing = dim.Render("—")
		}
		turn := "  "
		if m.state.Phase == "play" && m.state.Turn == i {
			turn = accent.Render("▶ ")
		}
		name := q.Name
		if i == m.you {
			name = mine.Render(name + " (you)")
			thing = mine.Render(thing)
		}
		tag := ""
		if q.Done {
			tag = " ✓"
		}
		if q.Off {
			tag += dim.Render(" (gone)")
		}
		fmt.Fprintf(&b, "%s%-28s %s%s\n", turn, name, thing, tag)
	}
	return b.String()
}

func (m model) log() string {
	lines := make([]string, len(m.state.Log))
	for i, l := range m.state.Log {
		if strings.HasSuffix(l, "✓") {
			lines[i] = win.Render(" " + l + " ")
		} else {
			lines[i] = dim.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

func (m model) View() string {
	line := dim.Render("  (nothing to type — you're taking notes)")
	help := "typing goes to notes · ctrl+c: quit"
	if m.active() {
		help = "tab: notes/input · ctrl+k: pass · ctrl+c: quit"
		if m.joined {
			m.entry.Placeholder = "your guess"
			if m.state.Phase == "assign" {
				m.entry.Placeholder = "what " + m.target().Name + " is"
			}
		}
		line = "> " + m.entry.View()
	}
	if m.you == 0 && m.joined {
		if m.state.Phase == "lobby" {
			help = "ctrl+s: start · " + help
		} else {
			help = "ctrl+r: new round · " + help
		}
	}

	left := lipgloss.JoinVertical(lipgloss.Left,
		accent.Render("HEADBAND"),
		"",
		m.status(),
		"",
		m.roster(),
		m.log(),
		"",
		line,
		bad.Render(m.err),
		dim.Render(help),
	)

	notes := box.Render(accent.Render("NOTES") + " " + dim.Render("(private, not saved)") + "\n" + m.notes.View())
	return lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(m.w*3/5).Padding(1, 2).Render(left), notes)
}

func main() {
	flag.StringVar(&port, "port", port, "port to host on, or to reach the host on")
	flag.Parse()

	addr := flag.Arg(0)
	if addr != "" && !strings.Contains(addr, ":") {
		addr += ":" + port
	}
	if _, err := tea.NewProgram(newModel(addr), tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
