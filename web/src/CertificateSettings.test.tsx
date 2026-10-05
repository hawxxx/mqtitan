import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CertificateSettings } from "./CertificateSettings";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  localStorage.clear();
  sessionStorage.clear();
});

it("uploads certificate files and retains only a profile reference", async () => {
  const selected = vi.fn();
  let uploaded: Record<string, string> = {};
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url, options) => {
      if (options?.method === "POST") {
        uploaded = JSON.parse(options.body);
        return new Response(
          JSON.stringify({
            data: {
              id: "certificate-profile",
              name: "Private lab",
              hasCa: true,
              hasClientCertificate: true,
              expiresAt: "2030-01-01T00:00:00Z",
              fingerprint: "abcdef",
            },
          }),
        );
      }
      return new Response(JSON.stringify({ data: [] }));
    }),
  );
  render(<CertificateSettings value={{}} onChange={selected} />);
  await userEvent.click(
    screen.getByRole("button", { name: "Upload certificates" }),
  );
  await userEvent.type(
    screen.getByLabelText("Certificate profile name"),
    "Private lab",
  );
  for (const [label, text] of [
    ["CA bundle (optional)", "ca-material"],
    ["Client certificate (optional)", "client-material"],
    ["Client private key (optional)", "private-key-material"],
  ]) {
    await userEvent.upload(
      screen.getByLabelText(label),
      new File([text], "file.pem", { type: "application/x-pem-file" }),
    );
  }
  await userEvent.click(
    screen.getByRole("button", { name: "Save certificate profile" }),
  );
  await waitFor(() =>
    expect(selected).toHaveBeenCalledWith({ profile: "certificate-profile" }),
  );
  expect(uploaded).toEqual({
    name: "Private lab",
    caPem: "ca-material",
    certPem: "client-material",
    keyPem: "private-key-material",
  });
  expect(screen.queryByText("private-key-material")).toBeNull();
  expect(JSON.stringify(localStorage)).not.toContain("private-key-material");
  expect(JSON.stringify(sessionStorage)).not.toContain("private-key-material");
  expect(
    (screen.getByLabelText("Client private key (optional)") as HTMLInputElement)
      .files?.length,
  ).toBe(0);
});

it("requires certificate and key together and rejects oversized files before uploading", async () => {
  const fetcher = vi.fn(
    async (_url: RequestInfo | URL, _options?: RequestInit) =>
      new Response(JSON.stringify({ data: [] })),
  );
  vi.stubGlobal("fetch", fetcher);
  render(<CertificateSettings value={{}} onChange={() => {}} />);
  await userEvent.click(
    screen.getByRole("button", { name: "Upload certificates" }),
  );
  await userEvent.type(
    screen.getByLabelText("Certificate profile name"),
    "Lab",
  );
  await userEvent.upload(
    screen.getByLabelText("Client certificate (optional)"),
    new File(["cert"], "cert.pem"),
  );
  await userEvent.click(
    screen.getByRole("button", { name: "Save certificate profile" }),
  );
  expect((await screen.findByRole("alert")).textContent).toContain(
    "certificate and private key together",
  );
  await userEvent.upload(
    screen.getByLabelText("CA bundle (optional)"),
    new File(["x".repeat(128 * 1024 + 1)], "big.pem"),
  );
  expect((await screen.findByRole("alert")).textContent).toContain("128 KiB");
  expect(
    fetcher.mock.calls.every((call) => !call[1] || call[1].method !== "POST"),
  ).toBe(true);
});

it("selects existing profiles without downloading private material", async () => {
  const selected = vi.fn();
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            data: [
              {
                id: "existing-profile",
                name: "Cluster client",
                hasCa: false,
                hasClientCertificate: true,
                expiresAt: "2030-01-01T00:00:00Z",
              },
            ],
          }),
        ),
    ),
  );
  render(<CertificateSettings value={{}} onChange={selected} />);
  await screen.findByRole("option", { name: /Cluster client/ });
  await userEvent.selectOptions(
    screen.getByLabelText("TLS certificate profile"),
    "existing-profile",
  );
  expect(selected).toHaveBeenCalledWith({ profile: "existing-profile" });
});
