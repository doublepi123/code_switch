package main

import (
	"fmt"
	"os"
)

var noColor = checkNoColor()

func checkNoColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return true
	}
	if os.Getenv("TERM") == "dumb" {
		return true
	}
	return false
}

func colorize(code, text string) string {
	if noColor {
		return text
	}
	return fmt.Sprintf("\x1b[%sm%s\x1b[0m", code, text)
}

func green(text string) string {
	return colorize("32", text)
}

func red(text string) string {
	return colorize("31", text)
}

func dim(text string) string {
	return colorize("2", text)
}

func bold(text string) string {
	return colorize("1", text)
}

func successPrefix(text string) string {
	if noColor {
		return "[OK] " + text
	}
	return green("[OK] ") + text
}

func formatLabel(label, value string) string {
	if noColor {
		return label + ": " + value
	}
	return dim(label+":") + " " + bold(value)
}