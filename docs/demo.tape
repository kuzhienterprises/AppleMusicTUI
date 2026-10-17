# The clip in the README. Run it from the repository root:
#
#	vhs docs/demo.tape
#
# It needs `am` on PATH and a signed-in session, and it plays music out loud
# while it records: the spectrum in the clip is the real one, read back off the
# system's audio through loopback capture. The clip itself is silent — only the
# picture is captured.
#
# Idle time costs nothing here. vhs only writes frames when the terminal
# changes, so the twenty seconds spent booting the hidden browser collapse into
# a couple of seconds of clip, while the spectrum — which is redrawn forty times
# a second — plays out at its real speed. The waits below are deliberately
# generous for that reason: the clip is cut down afterwards rather than guessed
# at while recording. It also means nothing else should be competing for the
# machine while this runs — a second browser booting at the same time can push
# the first one past its loading deadline.
Output "docs/demo.mp4"

Set Shell "pwsh"
Set FontFamily "Cascadia Mono"
Set FontSize 20
Set Width 1280
Set Height 720
Set Padding 18
Set Margin 26
Set MarginFill "#15151c"
Set BorderRadius 10
Set TypingSpeed 55ms
# vhs renders the terminal in a real browser, and that browser is another
# Chrome on the same machine as the hidden player. Twenty frames a second is
# plenty for a terminal and leaves the player more of the machine.
Set Framerate 20

Sleep 300ms
Hide
Type "cls"
Enter
Sleep 900ms
Show

# Search the catalogue and pick a result.
Type "am search 'instant crush'"
Sleep 300ms
Enter
Sleep 6s
Down
Sleep 700ms
Up
Sleep 700ms
Enter

# The engine comes up, and the track starts. A cold boot of the hidden browser
# is a genuinely slow thing — a minute is normal on a cold machine — so the
# warm-up runs below leave this take with a primed profile.
Sleep 150s

# Quieter, without leaving the player. These land either side of the first
# note, which is fine: MusicKit takes the volume whenever it is set.
Down@350ms 4
Sleep 40s

# Queue something else from inside the player, then get back to it.
Type "/"
Sleep 2s
Type "get lucky"
Sleep 4s
Enter
Sleep 5s
Down
Sleep 1s
Enter
Sleep 5s
Escape
Sleep 20s

# Skip to what was just queued.
Type "n"
Sleep 25s

# The queue, and out.
Type "a"
Sleep 8s
Type "a"
Sleep 3s
Type "q"
Sleep 3s
