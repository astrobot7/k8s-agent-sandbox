# Records hack/demo/remediation-demo.sh against the kind cluster:
#   vhs hack/demo/remediation-demo.tape
# Output goes to hack/demo/out/ (not committed).
Output hack/demo/out/remediation-demo.mp4
Output hack/demo/out/remediation-demo.gif

Require kubectl
Require jq

Set Shell "bash"
Set FontSize 15
Set Width 1400
Set Height 860
Set Padding 24
Set Theme "Catppuccin Mocha"
Set TypingSpeed 40ms
Set WindowBar Colorful

Hide
Type "export PS1='$ ' && clear"
Enter
Show

Type "hack/demo/remediation-demo.sh"
Sleep 500ms
Enter
Sleep 150s
Sleep 3s
