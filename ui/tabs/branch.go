package tabs

import (
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/aprokopczyk/mergemate/pkg/gitlab"
	"github.com/aprokopczyk/mergemate/ui/colors"
	"github.com/aprokopczyk/mergemate/ui/context"
	"github.com/aprokopczyk/mergemate/ui/keys"
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/evertras/bubble-table/table"
)

const lasCommitFormat = "2006-01-02 15:04:05"

const (
	columnKeyBranchName     = "branchName"
	columnKeyLastCommit     = "lastCommit"
	columnKeyBranchMetadata = "branchDetails"
)

type BranchTable struct {
	branchesList     list.Model
	flexTable        table.Model
	keys             keys.BranchKeyMap
	context          *context.AppContext
	showMergeTargets bool
}

type branchItem struct {
	name string
}

type MergeRequestCreated struct {
	mergeRequest gitlab.MergeRequestDetails
}

func (i branchItem) Title() string       { return i.name }
func (i branchItem) Description() string { return i.name }
func (i branchItem) FilterValue() string { return i.name }

func NewBranchTable(context *context.AppContext) *BranchTable {
	helpModel := help.New()
	helpModel.ShowAll = true
	return &BranchTable{
		flexTable: table.New([]table.Column{
			table.NewFlexColumn(columnKeyBranchName, "Branch", 15),
			table.NewFlexColumn(columnKeyLastCommit, "Last commit date", 15),
		}).WithRows([]table.Row{}).
			Focused(true).
			HeaderStyle(lipgloss.NewStyle().Bold(true)).
			WithBaseStyle(lipgloss.NewStyle().Align(lipgloss.Left).BorderForeground(colors.Emerald600)).
			WithPageSize(context.TablePageSize),
		branchesList:     createList(),
		keys:             keys.BranchHelp(context.FavouriteBranches),
		context:          context,
		showMergeTargets: false,
	}
}

func createList() list.Model {
	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = false
	model := list.New([]list.Item{}, delegate, 0, 20)
	model.Title = "Select target branch"
	model.DisableQuitKeybindings()
	model.SetShowStatusBar(false)
	model.SetShowHelp(false)
	return model
}

type UserBranches struct {
	branches []gitlab.Branch
}

type TargetBranches struct {
	Branches []gitlab.Branch
}

func (m *BranchTable) listUsersBranches() tea.Msg {
	branches := m.context.GitlabClient.FetchBranchesWithPattern([]string{m.context.UserBranchPrefix})

	return UserBranches{branches}
}

func (m *BranchTable) createMergeRequest(sourceBranch string, targetBranch string, title string) tea.Cmd {
	return func() tea.Msg {
		title = shortenTitle(title)
		mergeRequest, err := m.context.GitlabClient.CreateMergeRequest(sourceBranch, targetBranch, title)

		if errors.Is(err, gitlab.MergeRequestAlreadyExists) {
			return failed(fmt.Sprintf("merge request from branch %v already exists", sourceBranch))
		} else if errors.As(err, new(net.Error)) {
			return failed("merge request creation failed, please check your network connection")
		} else if err != nil {
			log.Printf("Error when creating merge request %v", err)
			return failed("unrecognized error when creating merge request, please check log file")
		}
		err = m.context.GitlabClient.CreateMergeRequestNote(mergeRequest.Iid, MergeAutomatically)
		if err != nil {
			log.Printf("Error when marking merge request to be merged automatically %v", err)
			return nil
		}
		return MergeRequestCreated{
			mergeRequest: *mergeRequest,
		}
	}
}

func shortenTitle(title string) string {
	idx := strings.IndexByte(title, '\n')
	if idx != -1 {
		title = title[:idx]
	}

	if len(title) > 255 {
		return title[:250] + "..."
	}

	return title
}

func (m *BranchTable) Init() tea.Cmd {
	return tea.Batch(m.listUsersBranches)
}

func (m *BranchTable) Update(msg tea.Msg) (TabContent, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)

	switch msg := msg.(type) {
	case UserBranches:
		var rows []table.Row
		branches := msg.branches
		for i := 0; i < len(branches); i++ {
			branch := branches[i]
			rows = append(rows, table.NewRow(table.RowData{
				columnKeyBranchName:     branch.Name,
				columnKeyLastCommit:     branch.Commit.AuthoredDate.In(time.Local).Format(lasCommitFormat),
				columnKeyBranchMetadata: branch,
			}))

		}
		m.flexTable = m.flexTable.WithRows(rows)
		m.flexTable = m.flexTable.PageFirst()
	case TargetBranches:
		var targetBranches []list.Item
		branches := msg.Branches
		for i := 0; i < len(branches); i++ {
			branch := branches[i]
			item := branchItem{name: branch.Name}
			if branch.Default {
				targetBranches = append([]list.Item{item}, targetBranches...)
			} else {
				targetBranches = append(targetBranches, item)
			}
		}
		m.branchesList.SetItems(targetBranches)
	case context.UpdatedContextMessage:
		m.recalculateComponents()
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.MergeAutomatically):
			if !m.showMergeTargets {
				m.changeBranchSelectionVisibility(true)
			}
		case key.Matches(msg, m.keys.CloseTargetBranchesList):
			if m.showMergeTargets && m.branchesList.FilterState() != list.Filtering {
				m.changeBranchSelectionVisibility(false)
			}
		case key.Matches(msg, m.keys.SelectTargetBranch):
			if m.showMergeTargets && m.branchesList.FilterState() != list.Filtering {
				sourceBranch := m.flexTable.HighlightedRow().Data[columnKeyBranchMetadata].(gitlab.Branch)
				targetBranch := m.branchesList.SelectedItem().(branchItem)
				cmds = append(cmds, m.createMergeRequest(sourceBranch.Name, targetBranch.name, sourceBranch.Commit.Message))
				m.changeBranchSelectionVisibility(false)
			}
		default:
			for i, binding := range m.keys.MergeFavourite {
				if key.Matches(msg, binding) && !m.showMergeTargets {
					sourceBranch := m.flexTable.HighlightedRow().Data[columnKeyBranchMetadata].(gitlab.Branch)
					cmds = append(cmds, m.createMergeRequest(sourceBranch.Name, m.context.FavouriteBranches[i], sourceBranch.Commit.Message))
				}
			}
		}
	}

	if !m.showMergeTargets {
		m.flexTable, cmd = m.flexTable.Update(msg)
		cmds = append(cmds, cmd)
	} else {
		m.branchesList, cmd = m.branchesList.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *BranchTable) changeBranchSelectionVisibility(visible bool) {
	m.keys.CloseTargetBranchesList.SetEnabled(visible)
	m.keys.SelectTargetBranch.SetEnabled(visible)
	m.keys.MergeAutomatically.SetEnabled(!visible)
	m.showMergeTargets = visible
	m.recalculateComponents()
	m.branchesList.ResetFilter()
	m.branchesList.ResetSelected()
	for i := range m.keys.MergeFavourite {
		m.keys.MergeFavourite[i].SetEnabled(!visible)
	}
}

func (m *BranchTable) recalculateComponents() {
	tableWidth := m.tableSize()
	m.flexTable = m.flexTable.WithTargetWidth(tableWidth)
	v := m.contentSize() - tableWidth
	m.branchesList.SetWidth(v)
	m.branchesList.SetHeight(m.context.TableContentHeight)
	m.flexTable = m.flexTable.WithPageSize(m.context.TablePageSize)
}

func (m *BranchTable) tableSize() int {
	contentSize := m.contentSize()
	if m.showMergeTargets {
		return int(float64(contentSize) * 0.7)
	}
	return contentSize
}

func (m *BranchTable) contentSize() int {
	var contentSize = m.context.WindowWidth - m.context.Styles.Tabs.Content.GetHorizontalFrameSize()
	return contentSize
}

func (m *BranchTable) FullHelp() []key.Binding {
	bindings := []key.Binding{
		m.keys.MergeAutomatically,
		m.keys.CloseTargetBranchesList,
		m.keys.SelectTargetBranch,
	}
	bindings = append(bindings, m.keys.MergeFavourite...)
	return bindings
}

func (m *BranchTable) View() string {
	if m.showMergeTargets {
		view := m.branchesList.View()
		lipgloss.Height(view)
		return lipgloss.JoinHorizontal(lipgloss.Top, m.flexTable.View(), view)
	}
	return m.flexTable.View()
}
