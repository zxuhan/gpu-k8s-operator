# vhs script for the README hero GIF.
# Two scenes, ~25s total — both tell the same story: the accounting
# engine is stateless, so the consumption counter survives an operator
# restart.
#   A. watch CONSUMED climb past the (tiny) quota; tracked pods at 8
#   B. delete the operator pod; watch the new one re-observe the same
#      8 pods and resume tracking — no state was persisted, none lost
#
# Assumes hack/demo/setup.sh has left a kind cluster in the
# "operator deployed, AlertOnly budget applied, no workload yet" state.
# Regenerate with: make demo.
#
# Uses `kubectl delete pod --wait=false` so the tape doesn't stall
# waiting for kubelet to confirm deletion, and skips `kubectl wait
# --for=condition=Ready` for the same reason (those blocking commands
# stretched an earlier cut of the GIF to 54s).

Output docs/media/demo.gif

Set Shell "bash"
Set FontSize 16
Set Width 1200
Set Height 700
Set Theme "Dracula"
Set TypingSpeed 25ms
Set PlaybackSpeed 1.0

Hide
Type "clear" Enter
Sleep 300ms
Show

# ─── Scene A: watch GPU-hour counter climb past quota ──────────────────
Type "# GPU-hour consumption climbs past the quota"
Enter
Sleep 700ms

Type "./bin/gwb-workload --namespace=demo --label=app=demo --count=8 --rate=3 --runtime=180s --gpus=100m --gpu-resource=cpu >/dev/null"
Enter
Sleep 2500ms

Type "kubectl get gwb -n demo demo -w -o custom-columns=CONSUMED:.status.consumedGpuHours,TRACKED:.status.trackedPods"
Enter
Sleep 7s
Ctrl+C
Sleep 300ms

# ─── Scene B: kill the operator → counter survives the restart ─────────
Type "clear"
Enter
Type "# kill the operator — accounting is stateless, counter survives"
Enter
Sleep 700ms

Type "kubectl delete pod -n gpu-k8s-operator-system -l control-plane=controller-manager --wait=false"
Enter
Sleep 1500ms

Type "kubectl get gwb -n demo demo -w -o custom-columns=CONSUMED:.status.consumedGpuHours,TRACKED:.status.trackedPods"
Enter
Sleep 9s
Ctrl+C
Sleep 300ms
