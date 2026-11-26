module github.com/assanoff/cmd/prr

go 1.27

require rsc.io/markdown v0.0.0-20241212154241-6bf72452917f

require golang.org/x/text v0.3.7 // indirect

// v0.1.0 carries two binaries committed by mistake; v0.1.1 is the same code without them.
retract v0.1.0
