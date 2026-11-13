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
	dupes web   [-http addr] [-n] [-rw]
	dupes forget path...
	dupes reset
	dupes mark  [-keep dir] [-dry-run] path...
	dupes unmark [-all] path...
	dupes apply [-dry-run] [-v]
	dupes history [-n count] [-f text|csv|json] [-o file]
	dupes restore number|path...
	dupes version

Every command takes -db, the database file. Only apply changes the disks,
and only by moving the files marked into a trash on their disk, which
restore moves them back from: dupes never deletes a file.

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

# Cleaning up

Cleaning up takes three steps, the first two of which change nothing on the
disks: mark the copies to get rid of, look at what is marked, and apply.

	dupes mark E:\old                       # what in E:\old is elsewhere too
	dupes mark -keep D:\Photos E:\backup    # what of E:\backup D:\Photos has
	dupes apply -dry-run                    # what would move, and what not and why
	dupes apply                             # move it
	dupes history                           # what was moved, numbered
	dupes restore 12                        # and back

A mark names the file to move and the copy of it that stays. The last copy
of a file is never marked: mark leaves alone a file whose every copy is in
the path or marked already, and -keep keeps the copies there even if they
were marked before. unmark takes marks back.

apply moves each marked file into .dupes-trash at the top of the scanned
path it is in, under the path it had, so D:\a\b.jpg goes to
D:\.dupes-trash\<time>\a\b.jpg: within one disk a move is instant, needs no
space and is undone by moving back, which restore does. The recycle bin of
each system works differently or, on some disks, not at all, and the trash
works on all of them alike. Scans skip it. To empty it, delete it yourself in
the file manager, which puts it in the recycle bin where there is one.

Before it moves a file, apply checks that the file and the copy that stays
are both where the scan saw them, unchanged in size and time, not one file
under two paths, such as a hard link or a disk scanned under two names, and
still the same content, read whole again. A file that fails any check stays,
and apply says why. -dry-run makes every check but the reading. A file is
never copied: one that cannot be moved within its disk stays too.

dupes web -rw does the same in the browser: a mark beside every file of a
group, "keep only this" to mark its other copies, "keep this one" on a
directory to mark what its copies have of it, and on Marked the rule of
mark -keep, the list, and the Check and Move buttons. History lists what was
moved and moves it back. Every page offers to copy a path and to show it in
the file manager: Explorer, Finder, or what xdg-open opens.

# Limits

Hard links are separate files to dupes, though deleting one frees nothing.
Files that cannot be read are counted, named with -v, and treated as unique;
the next scan tries them again.
*/
package main
