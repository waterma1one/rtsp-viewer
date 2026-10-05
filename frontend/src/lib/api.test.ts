import { describe, expect, it } from "vitest";
import { displayUrl, nameFromUrl, quickCheckUrl } from "./api";

describe("quickCheckUrl", () => {
  it.each([
    ["rtsp://cam.example.com/live", null],
    ["  RTSPS://cam.example.com:322/x  ", null],
    ["", "Enter an RTSP URL."],
    ["http://cam.example.com", "The URL must start with rtsp:// or rtsps://"],
    ["cam.example.com/live", "The URL must start with rtsp:// or rtsps://"],
  ])("%s", (input, expected) => {
    expect(quickCheckUrl(input)).toBe(expected);
  });
});

describe("nameFromUrl", () => {
  it("uses host and last path segment", () => {
    expect(nameFromUrl("rtsp://cam.example.com:554/site/garage")).toBe("cam.example.com / garage");
  });
  it("never includes credentials", () => {
    expect(nameFromUrl("rtsp://admin:hunter2@10.0.0.2/live")).not.toContain("hunter2");
  });
  it("falls back to the host", () => {
    expect(nameFromUrl("rtsp://cam.example.com")).toBe("cam.example.com");
  });
});

describe("displayUrl", () => {
  it("masks the password", () => {
    const shown = displayUrl("rtsp://admin:hunter2@cam.example.com/live");
    expect(shown).not.toContain("hunter2");
    expect(shown).toContain("admin");
  });
});
