package parser

import (
	"fmt"

	"github.com/odvcencio/gotreesitter"
)

func ParseSource(parser *gotreesitter.Parser, source []byte, relPath string) (*gotreesitter.Node, error) {
	tree, parseErr := parser.Parse(source)
	if parseErr != nil {
		return nil, fmt.Errorf("parse %s: %w", relPath, parseErr)
	}
	if tree == nil {
		return nil, fmt.Errorf("parser returned nil tree for %s", relPath)
	}

	root := tree.RootNode()
	if root == nil {
		return nil, fmt.Errorf("parse tree has nil root for %s", relPath)
	}

	return root, nil
}
