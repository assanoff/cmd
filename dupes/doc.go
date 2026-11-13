// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

/*
Dupes finds duplicate files and directories.

Usage:

	dupes scan  [-ext list] [-min size] [-x names] [-j n] [-q] [-v] path...
	dupes files [-by hash|name|namesize] [-sort waste|size|count] [-ext list] [-min size]
	            [-n count] [-f text|csv|json] [-o file] [path...]
	dupes dirs  [-by content|name] [-similar share] [-min size]
	            [-n count] [-f text|csv|json] [-o file] [path...]
	dupes stats
	dupes web   [-http addr] [-n]
	dupes forget path...
	dupes reset
	dupes version

Every command takes -db, the database file. It never deletes, moves or
changes a file: it finds, and the deleting is left to whoever reads the
report.

	dupes scan D:\ E:\Backup              # once, and again when the disks change
	dupes files                           # the ten groups that waste the most
	dupes files -n 100 -ext mp4,mkv       # the hundred among the videos
	dupes dirs                            # whole directories that are copies
	dupes dirs -by name -similar 0.9      # same-named ones that nearly are
	dupes files -n 0 -o dupes.csv         # all of it, for a spreadsheet
	dupes files -f json | jq ...          # or for a script

Reports go to standard output, progress and summaries to standard error.

# Scanning

scan walks each path and records every regular file that passes -ext and
-min — path, size, time — in a SQLite database, ~/.local/share/dupes/dupes.db
unless -db or $DUPES_DB says otherwise. Symbolic links and junctions are not
followed, so no tree is counted twice, and directories named by -x are
skipped: by default the recycle bin and System Volume Information. -min is 1
by default, since empty files are all copies of one another and deleting them
frees nothing.

Then it reads only what it has to, in two passes over the whole database:

  - A file whose size no other file has is unique, and is not read at all.
  - Files that share a size have their first and last 64 KB hashed; most
    files that differ, differ there.
  - Files that share a size and those ends are hashed whole, with SHA-256.

A path scanned before is brought up to date: a file whose size and time are
unchanged keeps its hash, a changed one is read again, and one that is gone
is forgotten. So a scan after the first costs a walk and the reading of what
is new. An interrupted scan keeps what it has done, and the next one does the
rest.

The database holds the files of the last scan of each path with the filters
of that scan: -min 1M forgets the smaller files that a scan without it
recorded. Scan with no filter to compare everything; the reports narrow it.

A path scanned before that is not there now — an unplugged drive — keeps its
files, and they are compared again once it is back.

# Files

files lists groups of files that are copies of one another, the most space to
reclaim first: the space deleting all but one of each would free. -sort size
puts the biggest files first, -sort count the most copies.

-by hash, the default, groups by content. -by name groups by name, ignoring
case; -by namesize by name and size. The name criteria read no file: they
find what may be copies, which is useful for files a scan has not hashed and
for copies that were edited since.

-ext and -min filter the report, so one full scan answers any number of
narrower questions. Paths after the flags keep only the groups that have a
file under one of them:

	dupes files E:\old       # what in E:\old has a copy somewhere

# Directories

dirs -by content, the default, lists groups of directories that hold the same
files, all of them, whatever their names and however they are arranged
inside. Any directory of a group can go without losing a file. Copies inside
copies are not listed again: when D:\a and E:\a are copies, so are D:\a\2019
and E:\a\2019, and only the first pair is shown.

A directory's content is the number, the bytes and the sum of the hashes of
every file below it, which is why one pass from the leaves up compares every
directory of a disk. It only sees the files the scan recorded: a directory
scanned with -ext jpg is a copy of another when their pictures are.

dirs -by name pairs directories of the same name and lists those with at
least -similar of their distinct files in common — the Photos of an old
backup that has nearly everything today's has, plus a few that it should keep.
A name more than -max-group directories share, such as src or .git, is
skipped: comparing every pair of them is slow and says little.

# Output

-n is how many groups to list, 10 by default and 0 for all. -f picks the
format: text for reading, csv with one row per file or directory, json as an
array of groups. -o writes to a file instead, and its extension picks the
format unless -f does.

# In a browser

	dupes web                 # http://localhost:8754/, opened in the browser
	dupes web -http :9000 -n  # another port, and no browser

web serves the reports as pages: an overview of what was scanned, the
duplicate files and directories with the filters of the files and dirs
commands, and a page for every scanned directory, which lists what is in it
and opens the other copies of a file or a directory under it. The CSV and
JSON of a report are a link away. The pages read the database afresh after
each scan, so web can keep running while a scan runs elsewhere.

It answers only to a browser on this machine, whatever -http says: the pages
list the files of every disk scanned. Its scripts are htmx 4, built into the
binary with the templates and the styles, so it needs no network either.

# Forgetting

forget drops a disk or a directory from the database, files and scanned
paths alike, and compares what is left again; the path need not exist any
more. reset deletes the database file itself, to start over. Neither touches
a file on the disks.

	dupes forget E:/          # the disk that was sold
	dupes reset               # everything

# Limits

Hard links are separate files to dupes, though deleting one frees nothing.
Files that cannot be read are counted, named with -v, and treated as unique;
the next scan tries them again.
*/
package main
