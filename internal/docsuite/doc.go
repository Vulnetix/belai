// Package docsuite holds tests only. They pin the numbers, defaults and rules
// that the pages under docs/ state to the constants and validators in the code,
// for facts that span packages and so have no single package to live in. A
// number changed in code without its page, or a page edited to a value the code
// does not hold, fails here. Nothing imports this package.
package docsuite
