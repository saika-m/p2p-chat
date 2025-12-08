package ui

import (
	"github.com/rivo/tview"
)

type InformationField struct {
	View *tview.TextView
}

func NewInformationField() *InformationField {
	view := tview.NewTextView().
		SetText("♡ " + "https://github.com/saika-m" + " ♡").
		SetTextAlign(tview.AlignCenter)

	view.SetTitle("saika-m/p2p-messenger").SetBorder(true)

	return &InformationField{
		View: view,
	}
}
