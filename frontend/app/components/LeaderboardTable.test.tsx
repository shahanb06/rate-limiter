import { fireEvent, render, screen } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import LeaderboardTable from "./LeaderboardTable";

test("clicking a row calls onSelect with that row's key", () => {
  const onSelect = vi.fn();
  const rows = [
    { key: "alpha", allowed: 10, rejected: 1, total: 11, rejection_rate: 0.0909 },
    { key: "bravo", allowed: 20, rejected: 5, total: 25, rejection_rate: 0.2 },
  ];

  render(<LeaderboardTable rows={rows} selected="alpha" onSelect={onSelect} />);

  fireEvent.click(screen.getByText("bravo"));
  expect(onSelect).toHaveBeenCalledTimes(1);
  expect(onSelect).toHaveBeenCalledWith("bravo");
});
