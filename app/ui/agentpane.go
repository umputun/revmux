package ui

func (m Model) agentLinesAt(tab int) []string {
	a := m.focusedAt(tab)
	if a == nil {
		return []string{"no such agent"}
	}
	if len(a.lines) == 0 {
		return []string{"waiting for " + a.spec.Name + "..."}
	}

	// wrapped and markdown-rendered as the combined log is, but left unpainted: one agent's pane has
	// nothing to attribute a row to. A model writes markdown into its prose and the lines it writes are
	// long; a forensic view that clips them is the one place a reader has gone looking for the detail,
	// so it is the last place to throw the end of it away.
	out := make([]string, 0, len(a.lines))
	for _, e := range a.lines {
		head := m.style.muted.Render(e.at.Format(timeFormat)) + " "
		out = append(out, Wrap(head, markdown(e.text), m.view.width())...)
	}
	return out
}

func (m Model) focusedAt(tab int) *agentState {
	i := tab - 1
	if i < 0 || i >= len(m.agents) {
		return nil
	}
	return m.agents[i]
}
