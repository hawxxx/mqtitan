import { useMutation } from "@tanstack/react-query";
import { Play } from "lucide-react";
import { request, type Test } from "./model";

export function RunAgainButton({
  test,
  onStarted,
}: {
  test: Test;
  onStarted: (run: Test) => void;
}) {
  const rerun = useMutation({
    mutationFn: () =>
      request<Test>(`/tests/${encodeURIComponent(test.id)}/rerun`, {
        method: "POST",
      }),
    onSuccess: (run) => onStarted(run),
  });
  if (!["stopped", "completed", "failed", "interrupted"].includes(test.status))
    return null;
  return (
    <div className="rerun-action">
      <button
        className="primary"
        type="button"
        disabled={rerun.isPending}
        title="Create a new run using the original scenario; keep these results unchanged"
        onClick={() => rerun.mutate()}
      >
        <Play size={14} aria-hidden="true" />{" "}
        {rerun.isPending ? "Starting…" : "Run again"}
      </button>
      {rerun.error && (
        <p className="rerun-error" role="alert">
          {rerun.error.message}
        </p>
      )}
    </div>
  );
}
