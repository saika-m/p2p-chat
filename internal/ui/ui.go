package ui

import (
	"fmt"
	"log"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"p2p-messenger/internal/crypto"
	"p2p-messenger/internal/entity"
	"p2p-messenger/internal/proto"
)

const (
	reprintFrequency = 50 * time.Millisecond
)

type App struct {
	Proto           *proto.Proto
	Chat            *Chat
	Sidebar         *Sidebar
	InfoField       *InformationField
	View            *tview.Pages
	UI              *tview.Application
	CurrentPeer     *entity.Peer
	tutorial        *tview.TextView
	tutorialVisible bool
}

func NewApp(proto *proto.Proto) *App {
	app := &App{
		Proto:           proto,
		Chat:            NewChat(),
		Sidebar:         NewSidebar(proto.Peers),
		InfoField:       NewInformationField(),
		View:            tview.NewPages(),
		UI:              tview.NewApplication(),
		CurrentPeer:     nil,
		tutorialVisible: false,
	}
	app.tutorial = newTutorialView()

	app.initView()
	app.initUI()
	app.initBindings()

	app.run()

	return app
}

func newTutorialView() *tview.TextView {
	view := tview.NewTextView()
	view.SetText(`Controls:
- Arrow keys: Navigate the peer list and messages
- Enter: Select a peer and start a chat
- j: Focus the message input field
- h: Focus the peer list
- Ctrl-T: Show/hide this tutorial`)
	view.SetBorder(true)
	view.SetTitle("Tutorial")
	return view
}

func (app *App) Run() error {
	return app.UI.SetRoot(app.View, true).SetFocus(app.Sidebar.View).Run()
}

func (app *App) initView() {
	mainView := tview.NewFlex().
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(app.InfoField.View, 3, 2, false).
			AddItem(app.Sidebar.View, 0, 1, false), 0, 1, false).
		AddItem(app.Chat.View, 0, 3, false)

	app.View.AddPage("main", mainView, true, true)
	app.View.AddPage("tutorial", app.tutorial, true, false)
}

func (app *App) initUI() {
	app.UI.SetRoot(app.View, true).SetFocus(app.Sidebar.View)
}

func (app *App) initBindings() {
	app.UI.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyCtrlT {
			app.toggleTutorial()
			return nil
		}
		return event
	})

	app.Sidebar.View.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Rune() == 'l' {
			app.UI.SetFocus(app.Chat.Messages)
		}

		if event.Key() == tcell.KeyEnter {
			if app.Sidebar.View.GetItemCount() > 0 {
				app.CurrentPeer = app.getCurrentPeer()
				app.UI.SetFocus(app.Chat.Messages)
			}
		}

		return event
	})

	app.Chat.Messages.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Rune() {
		case 'h':
			app.UI.SetFocus(app.Sidebar.View)
		case 'j':
			app.UI.SetFocus(app.Chat.InputField)
		}

		return event
	})

	app.Chat.InputField.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyUp {
			app.UI.SetFocus(app.Chat.Messages)
		}

		if event.Key() == tcell.KeyEnter {
			if app.CurrentPeer == nil {
				app.InfoField.View.SetText("Please select a peer to chat with")
				return event
			}
			if app.Chat.InputField.GetText() == "" {
				return event
			}

			message := app.Chat.InputField.GetText()
			peer := app.CurrentPeer

			// Use username for sent messages (fallback to peer ID if username not set)
			author := app.Proto.Username
			if author == "" {
				author = crypto.PeerID(app.Proto.PublicKey)
			}

			// Add message immediately to show on sender's screen
			peer.AddMessage(message, author)

			go func() {
				if err := peer.SendMessage(message, app.Proto.PrivateKey); err != nil {
					// Don't delete peer on temporary errors - log and continue
					// Only delete on permanent connection failures
					// The peer validator will handle cleanup of truly dead peers
					log.Printf("ui: failed to send message to %s: %v", peer.PeerID, err)
					// Don't clear the chat - let user see the message they sent
					// The peer validator will remove truly dead peers
				}
			}()

			app.Chat.InputField.SetText("")
		}

		return event
	})
}

func (app *App) toggleTutorial() {
	if app.tutorialVisible {
		app.View.SwitchToPage("main")
	} else {
		app.View.SwitchToPage("tutorial")
	}
	app.tutorialVisible = !app.tutorialVisible
}

func (app *App) renderMessages() {
	if app.CurrentPeer != nil {
		// Use current user's username/ID for author comparison
		currentUserID := app.Proto.Username
		if currentUserID == "" {
			currentUserID = crypto.PeerID(app.Proto.PublicKey)
		}
		app.Chat.RenderMessages(app.CurrentPeer.Messages, currentUserID)
		// Display full peer ID in title with connection type
		title := app.CurrentPeer.PeerID

		// Add connection type indicator
		if len(app.CurrentPeer.ConnectionTypes) > 0 {
			primaryType := app.CurrentPeer.PrimaryConnectionType.String()
			title = fmt.Sprintf("%s [%s]", title, primaryType)
		}

		app.Chat.View.SetTitle(title)
	}
}

func (app *App) getCurrentPeer() *entity.Peer {
	_, peerID := app.Sidebar.View.GetItemText(
		app.Sidebar.View.GetCurrentItem())

	peer, found := app.Proto.Peers.Get(peerID)
	if !found {
		return nil
	}

	return peer
}

func (app *App) run() {
	app.updateModeIndicators() // Initial update

	ticker := time.NewTicker(reprintFrequency)
	go func() {
		for {
			<-ticker.C
			app.UI.QueueUpdateDraw(app.Sidebar.Reprint)
			app.UI.QueueUpdateDraw(app.renderMessages)
		}
	}()

	networkTicker := time.NewTicker(1 * time.Second)
	go func() {
		for {
			<-networkTicker.C
			app.UI.QueueUpdateDraw(app.updateModeIndicators)
		}
	}()
}

func (app *App) updateModeIndicators() {
	if app.Proto.NetworkManager != nil {
		bleAvail, natAvail, internetAvail := app.Proto.NetworkManager.GetAvailableModes()
		app.InfoField.UpdateModes(bleAvail, natAvail, internetAvail)
	}
}
