// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ebnf

import (
	"go/scanner"
	"go/token"
	"os"
	"strconv"
)

type parser struct {
	fset *token.FileSet
	scanner.ErrorVector
	scanner scanner.Scanner
	pos     token.Pos   // token position
	tok     token.Token // one token look-ahead
	lit     string      // token literal
}

func (p *parser) next() {
	p.pos, p.tok, p.lit = p.scanner.Scan()
	if p.tok.IsKeyword() {
		// TODO Should keyword mapping always happen outside scanner?
		//      Or should there be a flag to scanner to enable keyword mapping?
		p.tok = token.IDENT
	}
}

func (p *parser) error(pos token.Pos, msg string) {
	p.Error(p.fset.Position(pos), msg)
}

func (p *parser) errorExpected(pos token.Pos, msg string) {
	msg = "expected " + msg
	if pos == p.pos {
		// the error happened at the current position;
		// make the error message more specific
		msg += ", found '" + p.tok.String() + "'"
		if p.tok.IsLiteral() {
			msg += " " + p.lit
		}
	}
	p.error(pos, msg)
}

func (p *parser) expect(tok token.Token) token.Pos {
	pos := p.pos
	if p.tok != tok {
		p.errorExpected(pos, "'"+tok.String()+"'")
	}
	p.next() // make progress in any case
	return pos
}

func (p *parser) parseIdentifier() *Name {
	pos := p.pos
	name := p.lit
	p.expect(token.IDENT)
	return &Name{pos, name}
}

func (p *parser) parseToken() *Token {
	pos := p.pos
	value := ""
	if p.tok == token.STRING {
		value, _ = strconv.Unquote(p.lit)
		// Unquote may fail with an error, but only if the scanner found
		// an illegal string in the first place. In this case the error
		// has already been reported.
		p.next()
	} else {
		p.expect(token.STRING)
	}
	return &Token{pos, value}
}

// ParseTerm returns nil if no term was found.
func (p *parser) parseTerm() (x Expression) {
	pos := p.pos

	switch p.tok {
	case token.IDENT:
		x = p.parseIdentifier()

	case token.STRING:
		tok := p.parseToken()
		x = tok
		const ellipsis = "…" // U+2026, the horizontal ellipsis character
		if p.tok == token.ILLEGAL && p.lit == ellipsis {
			p.next()
			x = &Range{tok, p.parseToken()}
		}

	case token.LPAREN:
		p.next()
		x = &Group{pos, p.parseExpression()}
		p.expect(token.RPAREN)

	case token.LBRACK:
		p.next()
		x = &Option{pos, p.parseExpression()}
		p.expect(token.RBRACK)

	case token.LBRACE:
		p.next()
		x = &Repetition{pos, p.parseExpression()}
		p.expect(token.RBRACE)
	}

	return x
}

func (p *parser) parseSequence() Expression {
	var list Sequence

	for x := p.parseTerm(); x != nil; x = p.parseTerm() {
		list = append(list, x)
	}

	// no need for a sequence if list.Len() < 2
	switch len(list) {
	case 0:
		p.errorExpected(p.pos, "term")
		return &Bad{p.pos, "term expected"}
	case 1:
		return list[0]
	}

	return list
}

func (p *parser) parseExpression() Expression {
	var list Alternative

	for {
		list = append(list, p.parseSequence())
		if p.tok != token.OR {
			break
		}
		p.next()
	}
	// len(list) > 0

	// no need for an Alternative node if list.Len() < 2
	if len(list) == 1 {
		return list[0]
	}

	return list
}

func (p *parser) parseProduction() *Production {
	name := p.parseIdentifier()
	p.expect(token.ASSIGN)
	var expr Expression
	if p.tok != token.PERIOD {
		expr = p.parseExpression()
	}
	p.expect(token.PERIOD)
	return &Production{name, expr}
}

func (p *parser) parse(fset *token.FileSet, filename string, src []byte) Grammar {
	// initialize parser
	p.fset = fset
	p.ErrorVector.Reset()
	p.scanner.Init(fset.AddFile(filename, fset.Base(), len(src)), src, p, scanner.AllowIllegalChars)
	p.next() // initializes pos, tok, lit

	grammar := make(Grammar)
	for p.tok != token.EOF {
		prod := p.parseProduction()
		name := prod.Name.String
		if _, found := grammar[name]; !found {
			grammar[name] = prod
		} else {
			p.error(prod.Pos(), name+" declared already")
		}
	}

	return grammar
}

// Parse parses a set of EBNF productions from source src.
// It returns a set of productions. Errors are reported
// for incorrect syntax and if a production is declared
// more than once. Position information is recorded relative
// to the file set fset.
//
func Parse(fset *token.FileSet, filename string, src []byte) (Grammar, os.Error) {
	var p parser
	grammar := p.parse(fset, filename, src)
	return grammar, p.GetError(scanner.Sorted)
}
