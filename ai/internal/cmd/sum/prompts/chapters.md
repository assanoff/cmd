Below is a transcript. Write a structured outline of what it covers.

Never reproduce the text. Every line you write must be your own sentence about
the text, shorter than the stretch it describes. If a section of your output is
as long as the part of the transcript it covers, you have copied rather than
summarized — cut it down.

Begin with two or three sentences on the whole recording. Write them before any
heading. Do not skip them and do not give them a heading of their own.

After those sentences, write nothing but sections. A section is a heading line
followed by bullets. Here is one, written for an unrelated recording about
database indexes so that its shape is clear and its words are plainly not
yours to reuse, the opening sentences included:

A walkthrough of how a relational database decides which index to use, from the
shape of a B-tree to the point where the planner gives up on one. Most of it is
about the cost model rather than the data structure.

## Why a B-tree beats a scan [00:01:30]

- A lookup walks the tree in logarithmic time instead of reading every row.
- The index costs write throughput, because every insert maintains it too.

## When the planner ignores an index [00:04:10]

- Past roughly a fifth of the table a sequential scan is cheaper, so it wins.

Your own headings and sentences take their place, drawn from the text below and
from nothing else. The times above are part of the drawing — take yours from
the text.

Every line starts at the left margin — never indent a heading, a bullet or a
sentence, because indented text is read as a code block. A heading is two hash
marks, one space, then the topic named in three to seven words; write the hash
marks once and never inside the topic name. Two to five bullets to a section,
one sentence each. Keep every fact, number, name, version and decision the text
states, and keep them right: if the text says which of two things does the
sending and which does the receiving, do not swap them.

Follow the order of the text. Aim for one section per distinct topic — for a
transcript this length that is usually between five and twelve sections, not
one and not thirty.

If the topic starts at a time marker in square brackets, such as [00:12:30],
end the heading with that marker, copied character for character from the text.
Never invent or adjust one. A topic with no marker near its start gets none.

Leave out everything that belongs to the recording rather than to its subject:
greetings, sign-offs, asides to the audience, repetition, and any advertising
or sponsor read. A stretch promoting a product that is not the subject of the
recording gets no section and no bullet — drop it entirely.

Where a stretch is a live demonstration rather than explanation — commands
being typed, a tool shown on screen — say so in its section and say what was
shown.

Write in {{.Lang}}.
