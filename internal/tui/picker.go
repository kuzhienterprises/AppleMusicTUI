package tui

import (
	"fmt"
	"io"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Item is one row in a picker list. Index refers to the position of the
// underlying value in the slice the caller passed in.
type Item struct {
	TitleText string
	Desc      string
	Index     int
}

func (i Item) Title() string       { return i.TitleText }
func (i Item) Description() string { return i.Desc }
func (i Item) FilterValue() string { return i.TitleText + " " + i.Desc }

type itemDelegate struct{ width int }

func (d itemDelegate) Height() int  { return 2 }
func (d itemDelegate) Spacing() int { return 0 }
func (d itemDelegate) Update(tea.Msg, *list.Model) tea.Cmd {
	return nil
}

func (d itemDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	entry, ok := item.(Item)
	if !ok {
		return
	}
	width := d.width - 6
	if width < 20 {
		width = 20
	}

	if index == m.Index() {
		fmt.Fprintf(w, "%s %s", cursorStyle.Render("▸"), selectedItem.Render(clip(entry.TitleText, width)))
	} else {
		fmt.Fprintf(w, "  %s", clip(entry.TitleText, width))
	}
	if entry.Desc != "" {
		fmt.Fprintf(w, "\n    %s", helpStyle.Render(clip(entry.Desc, width)))
	} else {
		fmt.Fprint(w, "\n")
	}
}

type pickerModel struct {
	list     list.Model
	choice   *Item
	quitting bool
	width    int
	height   int
}

func (m pickerModel) Init() tea.Cmd { return nil }

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

		// Keep the list a comfortable reading width and let centring take
		// care of the rest, rather than stretching it across the window.
		listWidth := clampInt(msg.Width-8, 40, 90)
		m.list.SetSize(listWidth, clampInt(msg.Height-8, 6, 24))
		m.list.SetDelegate(itemDelegate{width: listWidth})
		return m, nil

	case tea.KeyMsg:
		// While filtering, let the list consume ordinary keys.
		if m.list.FilterState() == list.Filtering && msg.String() != "enter" {
			break
		}
		switch msg.String() {
		case "enter":
			if entry, ok := m.list.SelectedItem().(Item); ok {
				selected := entry
				m.choice = &selected
			}
			m.quitting = true
			return m, tea.Quit
		case "q", "esc", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m pickerModel) View() string {
	if m.quitting {
		return ""
	}

	body := m.list.View() + "\n" + helpStyle.Render("enter select · / filter · esc cancel") + "\n"
	if m.width <= 0 || m.height <= 0 {
		return body
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, body)
}

// Choose shows an interactive list and returns the chosen item, or nil when
// the user cancels.
func Choose(items []Item, title string) (*Item, error) {
	if len(items) == 0 {
		return nil, nil
	}

	rows := make([]list.Item, len(items))
	for i, item := range items {
		rows[i] = item
	}

	width, height := 80, 20
	delegate := itemDelegate{width: width}
	model := list.New(rows, delegate, width, height)
	model.Title = title
	model.SetShowStatusBar(false)
	model.SetFilteringEnabled(true)
	model.SetShowHelp(false)
	model.Styles.Title = pickerTitle

	program := tea.NewProgram(pickerModel{list: model}, tea.WithAltScreen())
	final, err := program.Run()
	if err != nil {
		return nil, err
	}
	if result, ok := final.(pickerModel); ok {
		return result.choice, nil
	}
	return nil, nil
}
