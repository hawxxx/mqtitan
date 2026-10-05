import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RunAgainButton } from "./RunAgainButton";
import type { Test } from "./model";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
const previous = { id: "old-run", status: "stopped" } as Test;
function show(status = "stopped", onStarted = vi.fn()) {
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { mutations: { retry: false } } })
      }
    >
      <RunAgainButton test={{ ...previous, status }} onStarted={onStarted} />
    </QueryClientProvider>,
  );
  return onStarted;
}

it("starts a new server-side run without copying redacted scenarios", async () => {
  const next = {
    ...previous,
    id: "new-run",
    status: "running",
    sourceTestId: previous.id,
  };
  const fetcher = vi.fn(
    async (_input: RequestInfo | URL, _init?: RequestInit) =>
      new Response(JSON.stringify({ data: next })),
  );
  vi.stubGlobal("fetch", fetcher);
  const started = show();
  expect(fetcher).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Run again" }));
  await waitFor(() => expect(started).toHaveBeenCalledWith(next));
  expect(fetcher.mock.calls[0][0]).toBe("/api/v1/tests/old-run/rerun");
  expect(fetcher.mock.calls[0][1]).toMatchObject({ method: "POST" });
  expect(fetcher.mock.calls[0][1]?.body).toBeUndefined();
});

it("only offers reruns for terminal statuses", () => {
  for (const status of ["completed", "stopped", "failed", "interrupted"]) {
    show(status);
    expect(screen.getByRole("button", { name: "Run again" })).toBeTruthy();
    cleanup();
  }
  for (const status of ["running", "pending", "stopping"]) {
    show(status);
    expect(screen.queryByRole("button", { name: "Run again" })).toBeNull();
    cleanup();
  }
});

it("disables duplicate clicks while the new run is starting", async () => {
  let release!: (value: Response) => void;
  const fetcher = vi.fn(
    () =>
      new Promise<Response>((resolve) => {
        release = resolve;
      }),
  );
  vi.stubGlobal("fetch", fetcher);
  show();
  await userEvent.click(screen.getByRole("button", { name: "Run again" }));
  const pending = await screen.findByRole("button", { name: "Starting…" });
  expect((pending as HTMLButtonElement).disabled).toBe(true);
  await userEvent.click(pending);
  expect(fetcher).toHaveBeenCalledTimes(1);
  release(
    new Response(JSON.stringify({ data: { ...previous, id: "new-run" } })),
  );
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "Run again" })).toBeTruthy(),
  );
});

it("shows conflicts and leaves the previous result open", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            error: {
              message:
                "A test is already running; stop it before starting another",
            },
          }),
          { status: 409 },
        ),
    ),
  );
  const started = show();
  await userEvent.click(screen.getByRole("button", { name: "Run again" }));
  expect((await screen.findByRole("alert")).textContent).toContain(
    "already running",
  );
  expect(started).not.toHaveBeenCalled();
  expect(
    (screen.getByRole("button", { name: "Run again" }) as HTMLButtonElement)
      .disabled,
  ).toBe(false);
});
