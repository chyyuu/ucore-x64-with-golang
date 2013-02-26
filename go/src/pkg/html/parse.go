// Copyright 2010 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package html

import (
	"io"
	"os"
)

// A parser implements the HTML5 parsing algorithm:
// http://www.whatwg.org/specs/web-apps/current-work/multipage/tokenization.html#tree-construction
type parser struct {
	// tokenizer provides the tokens for the parser.
	tokenizer *Tokenizer
	// tok is the most recently read token.
	tok Token
	// Self-closing tags like <hr/> are re-interpreted as a two-token sequence:
	// <hr> followed by </hr>. hasSelfClosingToken is true if we have just read
	// the synthetic start tag and the next one due is the matching end tag.
	hasSelfClosingToken bool
	// doc is the document root element.
	doc *Node
	// The stack of open elements (section 11.2.3.2) and active formatting
	// elements (section 11.2.3.3).
	oe, afe nodeStack
	// Element pointers (section 11.2.3.4).
	head, form *Node
	// Other parsing state flags (section 11.2.3.5).
	scripting, framesetOK bool
}

func (p *parser) top() *Node {
	if n := p.oe.top(); n != nil {
		return n
	}
	return p.doc
}

// stopTags for use in popUntil. These come from section 11.2.3.2.
var (
	defaultScopeStopTags  = []string{"applet", "caption", "html", "table", "td", "th", "marquee", "object"}
	listItemScopeStopTags = []string{"applet", "caption", "html", "table", "td", "th", "marquee", "object", "ol", "ul"}
	buttonScopeStopTags   = []string{"applet", "caption", "html", "table", "td", "th", "marquee", "object", "button"}
	tableScopeStopTags    = []string{"html", "table"}
)

// popUntil pops the stack of open elements at the highest element whose tag
// is in matchTags, provided there is no higher element in stopTags. It returns
// whether or not there was such an element. If there was not, popUntil leaves
// the stack unchanged.
//
// For example, if the stack was:
// ["html", "body", "font", "table", "b", "i", "u"]
// then popUntil([]string{"html, "table"}, "font") would return false, but
// popUntil([]string{"html, "table"}, "i") would return true and the resultant
// stack would be:
// ["html", "body", "font", "table", "b"]
//
// If an element's tag is in both stopTags and matchTags, then the stack will
// be popped and the function returns true (provided, of course, there was no
// higher element in the stack that was also in stopTags). For example,
// popUntil([]string{"html, "table"}, "table") would return true and leave:
// ["html", "body", "font"]
func (p *parser) popUntil(stopTags []string, matchTags ...string) bool {
	for i := len(p.oe) - 1; i >= 0; i-- {
		tag := p.oe[i].Data
		for _, t := range matchTags {
			if t == tag {
				p.oe = p.oe[:i]
				return true
			}
		}
		for _, t := range stopTags {
			if t == tag {
				return false
			}
		}
	}
	return false
}

// addChild adds a child node n to the top element, and pushes n onto the stack
// of open elements if it is an element node.
func (p *parser) addChild(n *Node) {
	p.top().Add(n)
	if n.Type == ElementNode {
		p.oe = append(p.oe, n)
	}
}

// addText adds text to the preceding node if it is a text node, or else it
// calls addChild with a new text node.
func (p *parser) addText(text string) {
	// TODO: distinguish whitespace text from others.
	t := p.top()
	if i := len(t.Child); i > 0 && t.Child[i-1].Type == TextNode {
		t.Child[i-1].Data += text
		return
	}
	p.addChild(&Node{
		Type: TextNode,
		Data: text,
	})
}

// addElement calls addChild with an element node.
func (p *parser) addElement(tag string, attr []Attribute) {
	p.addChild(&Node{
		Type: ElementNode,
		Data: tag,
		Attr: attr,
	})
}

// Section 11.2.3.3.
func (p *parser) addFormattingElement(tag string, attr []Attribute) {
	p.addElement(tag, attr)
	p.afe = append(p.afe, p.top())
	// TODO.
}

// Section 11.2.3.3.
func (p *parser) clearActiveFormattingElements() {
	for {
		n := p.afe.pop()
		if len(p.afe) == 0 || n.Type == scopeMarkerNode {
			return
		}
	}
}

// Section 11.2.3.3.
func (p *parser) reconstructActiveFormattingElements() {
	n := p.afe.top()
	if n == nil {
		return
	}
	if n.Type == scopeMarkerNode || p.oe.index(n) != -1 {
		return
	}
	i := len(p.afe) - 1
	for n.Type != scopeMarkerNode && p.oe.index(n) == -1 {
		if i == 0 {
			i = -1
			break
		}
		i--
		n = p.afe[i]
	}
	for {
		i++
		n = p.afe[i]
		p.addChild(n.clone())
		p.afe[i] = n
		if i == len(p.afe)-1 {
			break
		}
	}
}

// read reads the next token. This is usually from the tokenizer, but it may
// be the synthesized end tag implied by a self-closing tag.
func (p *parser) read() os.Error {
	if p.hasSelfClosingToken {
		p.hasSelfClosingToken = false
		p.tok.Type = EndTagToken
		p.tok.Attr = nil
		return nil
	}
	p.tokenizer.Next()
	p.tok = p.tokenizer.Token()
	switch p.tok.Type {
	case ErrorToken:
		return p.tokenizer.Error()
	case SelfClosingTagToken:
		p.hasSelfClosingToken = true
		p.tok.Type = StartTagToken
	}
	return nil
}

// Section 11.2.4.
func (p *parser) acknowledgeSelfClosingTag() {
	p.hasSelfClosingToken = false
}

// An insertion mode (section 11.2.3.1) is the state transition function from
// a particular state in the HTML5 parser's state machine. It updates the
// parser's fields depending on parser.token (where ErrorToken means EOF). In
// addition to returning the next insertionMode state, it also returns whether
// the token was consumed.
type insertionMode func(*parser) (insertionMode, bool)

// useTheRulesFor runs the delegate insertionMode over p, returning the actual
// insertionMode unless the delegate caused a state transition.
// Section 11.2.3.1, "using the rules for".
func useTheRulesFor(p *parser, actual, delegate insertionMode) (insertionMode, bool) {
	im, consumed := delegate(p)
	if im != delegate {
		return im, consumed
	}
	return actual, consumed
}

// Section 11.2.5.4.1.
func initialIM(p *parser) (insertionMode, bool) {
	if p.tok.Type == DoctypeToken {
		p.addChild(&Node{
			Type: DoctypeNode,
			Data: p.tok.Data,
		})
		return beforeHTMLIM, true
	}
	// TODO: set "quirks mode"? It's defined in the DOM spec instead of HTML5 proper,
	// and so switching on "quirks mode" might belong in a different package.
	return beforeHTMLIM, false
}

// Section 11.2.5.4.2.
func beforeHTMLIM(p *parser) (insertionMode, bool) {
	var (
		add     bool
		attr    []Attribute
		implied bool
	)
	switch p.tok.Type {
	case ErrorToken:
		implied = true
	case TextToken:
		// TODO: distinguish whitespace text from others.
		implied = true
	case StartTagToken:
		if p.tok.Data == "html" {
			add = true
			attr = p.tok.Attr
		} else {
			implied = true
		}
	case EndTagToken:
		switch p.tok.Data {
		case "head", "body", "html", "br":
			implied = true
		default:
			// Ignore the token.
		}
	}
	if add || implied {
		p.addElement("html", attr)
	}
	return beforeHeadIM, !implied
}

// Section 11.2.5.4.3.
func beforeHeadIM(p *parser) (insertionMode, bool) {
	var (
		add     bool
		attr    []Attribute
		implied bool
	)
	switch p.tok.Type {
	case ErrorToken:
		implied = true
	case TextToken:
		// TODO: distinguish whitespace text from others.
		implied = true
	case StartTagToken:
		switch p.tok.Data {
		case "head":
			add = true
			attr = p.tok.Attr
		case "html":
			return useTheRulesFor(p, beforeHeadIM, inBodyIM)
		default:
			implied = true
		}
	case EndTagToken:
		switch p.tok.Data {
		case "head", "body", "html", "br":
			implied = true
		default:
			// Ignore the token.
		}
	}
	if add || implied {
		p.addElement("head", attr)
	}
	return inHeadIM, !implied
}

// Section 11.2.5.4.4.
func inHeadIM(p *parser) (insertionMode, bool) {
	var (
		pop     bool
		implied bool
	)
	switch p.tok.Type {
	case ErrorToken, TextToken:
		implied = true
	case StartTagToken:
		switch p.tok.Data {
		case "meta":
			// TODO.
		case "script":
			// TODO.
		default:
			implied = true
		}
	case EndTagToken:
		if p.tok.Data == "head" {
			pop = true
		}
		// TODO.
	}
	if pop || implied {
		n := p.oe.pop()
		if n.Data != "head" {
			panic("html: bad parser state")
		}
		return afterHeadIM, !implied
	}
	return inHeadIM, !implied
}

// Section 11.2.5.4.6.
func afterHeadIM(p *parser) (insertionMode, bool) {
	var (
		add        bool
		attr       []Attribute
		framesetOK bool
		implied    bool
	)
	switch p.tok.Type {
	case ErrorToken, TextToken:
		implied = true
		framesetOK = true
	case StartTagToken:
		switch p.tok.Data {
		case "html":
			// TODO.
		case "body":
			add = true
			attr = p.tok.Attr
			framesetOK = false
		case "frameset":
			// TODO.
		case "base", "basefont", "bgsound", "link", "meta", "noframes", "script", "style", "title":
			// TODO.
		case "head":
			// TODO.
		default:
			implied = true
			framesetOK = true
		}
	case EndTagToken:
		// TODO.
	}
	if add || implied {
		p.addElement("body", attr)
		p.framesetOK = framesetOK
	}
	return inBodyIM, !implied
}

// Section 11.2.5.4.7.
func inBodyIM(p *parser) (insertionMode, bool) {
	var endP bool
	switch p.tok.Type {
	case TextToken:
		p.reconstructActiveFormattingElements()
		p.addText(p.tok.Data)
		p.framesetOK = false
	case StartTagToken:
		switch p.tok.Data {
		case "address", "article", "aside", "blockquote", "center", "details", "dir", "div", "dl", "fieldset", "figcaption", "figure", "footer", "header", "hgroup", "menu", "nav", "ol", "p", "section", "summary", "ul":
			// TODO: Do the proper "does the stack of open elements has a p element in button scope" algorithm in section 11.2.3.2.
			n := p.top()
			if n.Type == ElementNode && n.Data == "p" {
				endP = true
			} else {
				p.addElement(p.tok.Data, p.tok.Attr)
			}
		case "h1", "h2", "h3", "h4", "h5", "h6":
			// TODO: auto-insert </p> if necessary.
			switch n := p.top(); n.Data {
			case "h1", "h2", "h3", "h4", "h5", "h6":
				p.oe.pop()
			}
			p.addElement(p.tok.Data, p.tok.Attr)
		case "a":
			if n := p.afe.forTag("a"); n != nil {
				p.inBodyEndTagFormatting("a")
				p.oe.remove(n)
				p.afe.remove(n)
			}
			p.reconstructActiveFormattingElements()
			p.addFormattingElement(p.tok.Data, p.tok.Attr)
		case "b", "big", "code", "em", "font", "i", "s", "small", "strike", "strong", "tt", "u":
			p.reconstructActiveFormattingElements()
			p.addFormattingElement(p.tok.Data, p.tok.Attr)
		case "area", "br", "embed", "img", "input", "keygen", "wbr":
			p.reconstructActiveFormattingElements()
			p.addElement(p.tok.Data, p.tok.Attr)
			p.oe.pop()
			p.acknowledgeSelfClosingTag()
			p.framesetOK = false
		case "table":
			// TODO: auto-insert </p> if necessary, depending on quirks mode.
			p.addElement(p.tok.Data, p.tok.Attr)
			p.framesetOK = false
			return inTableIM, true
		case "hr":
			// TODO: auto-insert </p> if necessary.
			p.addElement(p.tok.Data, p.tok.Attr)
			p.oe.pop()
			p.acknowledgeSelfClosingTag()
			p.framesetOK = false
		default:
			// TODO.
			p.addElement(p.tok.Data, p.tok.Attr)
		}
	case EndTagToken:
		switch p.tok.Data {
		case "body":
			// TODO: autoclose the stack of open elements.
			return afterBodyIM, true
		case "a", "b", "big", "code", "em", "font", "i", "nobr", "s", "small", "strike", "strong", "tt", "u":
			p.inBodyEndTagFormatting(p.tok.Data)
		default:
			// TODO: any other end tag
			if p.tok.Data == p.top().Data {
				p.oe.pop()
			}
		}
	}
	if endP {
		// TODO: do the proper algorithm.
		n := p.oe.pop()
		if n.Type != ElementNode || n.Data != "p" {
			panic("unreachable")
		}
	}
	return inBodyIM, !endP
}

func (p *parser) inBodyEndTagFormatting(tag string) {
	// This is the "adoption agency" algorithm, described at
	// http://www.whatwg.org/specs/web-apps/current-work/multipage/tokenization.html#adoptionAgency

	// TODO: this is a fairly literal line-by-line translation of that algorithm.
	// Once the code successfully parses the comprehensive test suite, we should
	// refactor this code to be more idiomatic.

	// Steps 1-3. The outer loop.
	for i := 0; i < 8; i++ {
		// Step 4. Find the formatting element.
		var formattingElement *Node
		for j := len(p.afe) - 1; j >= 0; j-- {
			if p.afe[j].Type == scopeMarkerNode {
				break
			}
			if p.afe[j].Data == tag {
				formattingElement = p.afe[j]
				break
			}
		}
		if formattingElement == nil {
			return
		}
		feIndex := p.oe.index(formattingElement)
		if feIndex == -1 {
			p.afe.remove(formattingElement)
			return
		}

		// Steps 5-6. Find the furthest block.
		var furthestBlock *Node
		for _, e := range p.oe[feIndex:] {
			if isSpecialElement[e.Data] {
				furthestBlock = e
				break
			}
		}
		if furthestBlock == nil {
			e := p.oe.pop()
			for e != formattingElement {
				e = p.oe.pop()
			}
			p.afe.remove(e)
			return
		}

		// Steps 7-8. Find the common ancestor and bookmark node.
		commonAncestor := p.oe[feIndex-1]
		bookmark := p.afe.index(formattingElement)

		// Step 9. The inner loop. Find the lastNode to reparent.
		lastNode := furthestBlock
		node := furthestBlock
		x := p.oe.index(node)
		// Steps 9.1-9.3.
		for j := 0; j < 3; j++ {
			// Step 9.4.
			x--
			node = p.oe[x]
			// Step 9.5.
			if p.afe.index(node) == -1 {
				p.oe.remove(node)
				continue
			}
			// Step 9.6.
			if node == formattingElement {
				break
			}
			// Step 9.7.
			clone := node.clone()
			p.afe[p.afe.index(node)] = clone
			p.oe[p.oe.index(node)] = clone
			node = clone
			// Step 9.8.
			if lastNode == furthestBlock {
				bookmark = p.afe.index(node) + 1
			}
			// Step 9.9.
			if lastNode.Parent != nil {
				lastNode.Parent.Remove(lastNode)
			}
			node.Add(lastNode)
			// Step 9.10.
			lastNode = node
		}

		// Step 10. Reparent lastNode to the common ancestor,
		// or for misnested table nodes, to the foster parent.
		if lastNode.Parent != nil {
			lastNode.Parent.Remove(lastNode)
		}
		switch commonAncestor.Data {
		case "table", "tbody", "tfoot", "thead", "tr":
			// TODO: fix up misnested table nodes; find the foster parent.
			fallthrough
		default:
			commonAncestor.Add(lastNode)
		}

		// Steps 11-13. Reparent nodes from the furthest block's children
		// to a clone of the formatting element.
		clone := formattingElement.clone()
		reparentChildren(clone, furthestBlock)
		furthestBlock.Add(clone)

		// Step 14. Fix up the list of active formatting elements.
		p.afe.remove(formattingElement)
		p.afe.insert(bookmark, clone)

		// Step 15. Fix up the stack of open elements.
		p.oe.remove(formattingElement)
		p.oe.insert(p.oe.index(furthestBlock)+1, clone)
	}
}

// Section 11.2.5.4.9.
func inTableIM(p *parser) (insertionMode, bool) {
	var (
		add      bool
		data     string
		attr     []Attribute
		consumed bool
	)
	switch p.tok.Type {
	case ErrorToken:
		// Stop parsing.
		return nil, true
	case TextToken:
		// TODO.
	case StartTagToken:
		switch p.tok.Data {
		case "tbody", "tfoot", "thead":
			add = true
			data = p.tok.Data
			attr = p.tok.Attr
			consumed = true
		case "td", "th", "tr":
			add = true
			data = "tbody"
		default:
			// TODO.
		}
	case EndTagToken:
		switch p.tok.Data {
		case "table":
			if p.popUntil(tableScopeStopTags, "table") {
				// TODO: "reset the insertion mode appropriately" as per 11.2.3.1.
				return inBodyIM, false
			}
			// Ignore the token.
			return inTableIM, true
		case "body", "caption", "col", "colgroup", "html", "tbody", "td", "tfoot", "th", "thead", "tr":
			// Ignore the token.
			return inTableIM, true
		}
	}
	if add {
		// TODO: clear the stack back to a table context.
		p.addElement(data, attr)
		return inTableBodyIM, consumed
	}
	// TODO: return useTheRulesFor(inTableIM, inBodyIM, p) unless etc. etc. foster parenting.
	return inTableIM, true
}

// Section 11.2.5.4.13.
func inTableBodyIM(p *parser) (insertionMode, bool) {
	var (
		add      bool
		data     string
		attr     []Attribute
		consumed bool
	)
	switch p.tok.Type {
	case ErrorToken:
		// TODO.
	case TextToken:
		// TODO.
	case StartTagToken:
		switch p.tok.Data {
		case "tr":
			add = true
			data = p.tok.Data
			attr = p.tok.Attr
			consumed = true
		case "td", "th":
			add = true
			data = "tr"
			consumed = false
		default:
			// TODO.
		}
	case EndTagToken:
		switch p.tok.Data {
		case "table":
			if p.popUntil(tableScopeStopTags, "tbody", "thead", "tfoot") {
				return inTableIM, false
			}
			// Ignore the token.
			return inTableBodyIM, true
		case "body", "caption", "col", "colgroup", "html", "td", "th", "tr":
			// Ignore the token.
			return inTableBodyIM, true
		}
	}
	if add {
		// TODO: clear the stack back to a table body context.
		p.addElement(data, attr)
		return inRowIM, consumed
	}
	return useTheRulesFor(p, inTableBodyIM, inTableIM)
}

// Section 11.2.5.4.14.
func inRowIM(p *parser) (insertionMode, bool) {
	switch p.tok.Type {
	case ErrorToken:
		// TODO.
	case TextToken:
		// TODO.
	case StartTagToken:
		switch p.tok.Data {
		case "td", "th":
			// TODO: clear the stack back to a table row context.
			p.addElement(p.tok.Data, p.tok.Attr)
			p.afe = append(p.afe, &scopeMarker)
			return inCellIM, true
		default:
			// TODO.
		}
	case EndTagToken:
		switch p.tok.Data {
		case "tr":
			// TODO.
		case "table":
			if p.popUntil(tableScopeStopTags, "tr") {
				return inTableBodyIM, false
			}
			// Ignore the token.
			return inRowIM, true
		case "tbody", "tfoot", "thead":
			// TODO.
		case "body", "caption", "col", "colgroup", "html", "td", "th":
			// Ignore the token.
			return inRowIM, true
		default:
			// TODO.
		}
	}
	return useTheRulesFor(p, inRowIM, inTableIM)
}

// Section 11.2.5.4.15.
func inCellIM(p *parser) (insertionMode, bool) {
	var (
		closeTheCellAndReprocess bool
	)
	switch p.tok.Type {
	case StartTagToken:
		switch p.tok.Data {
		case "caption", "col", "colgroup", "tbody", "td", "tfoot", "th", "thead", "tr":
			// TODO: check for "td" or "th" in table scope.
			closeTheCellAndReprocess = true
		}
	case EndTagToken:
		switch p.tok.Data {
		case "td", "th":
			// TODO.
		case "body", "caption", "col", "colgroup", "html":
			// TODO.
		case "table", "tbody", "tfoot", "thead", "tr":
			// TODO: check for matching element in table scope.
			closeTheCellAndReprocess = true
		}
	}
	if closeTheCellAndReprocess {
		if p.popUntil(tableScopeStopTags, "td") || p.popUntil(tableScopeStopTags, "th") {
			p.clearActiveFormattingElements()
			return inRowIM, false
		}
	}
	return useTheRulesFor(p, inCellIM, inBodyIM)
}

// Section 11.2.5.4.18.
func afterBodyIM(p *parser) (insertionMode, bool) {
	switch p.tok.Type {
	case ErrorToken:
		// TODO.
	case TextToken:
		// TODO.
	case StartTagToken:
		// TODO.
	case EndTagToken:
		switch p.tok.Data {
		case "html":
			// TODO: autoclose the stack of open elements.
			return afterAfterBodyIM, true
		default:
			// TODO.
		}
	}
	return afterBodyIM, true
}

// Section 11.2.5.4.21.
func afterAfterBodyIM(p *parser) (insertionMode, bool) {
	switch p.tok.Type {
	case ErrorToken:
		// Stop parsing.
		return nil, true
	case TextToken:
		// TODO.
	case StartTagToken:
		if p.tok.Data == "html" {
			return useTheRulesFor(p, afterAfterBodyIM, inBodyIM)
		}
	}
	return inBodyIM, false
}

// Parse returns the parse tree for the HTML from the given Reader.
// The input is assumed to be UTF-8 encoded.
func Parse(r io.Reader) (*Node, os.Error) {
	p := &parser{
		tokenizer: NewTokenizer(r),
		doc: &Node{
			Type: DocumentNode,
		},
		scripting:  true,
		framesetOK: true,
	}
	// Iterate until EOF. Any other error will cause an early return.
	im, consumed := initialIM, true
	for {
		if consumed {
			if err := p.read(); err != nil {
				if err == os.EOF {
					break
				}
				return nil, err
			}
		}
		im, consumed = im(p)
	}
	// Loop until the final token (the ErrorToken signifying EOF) is consumed.
	for {
		if im, consumed = im(p); consumed {
			break
		}
	}
	return p.doc, nil
}
