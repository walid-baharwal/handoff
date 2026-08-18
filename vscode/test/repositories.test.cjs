"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const { compatibleTarget, groupHandoffs, matchingRepositories } = require("../lib/repositories");

test("matches handoffs by stable repository identity", () => {
  const repositories = [
    { root: "/workspace/api", repository_id: "api" },
    { root: "/workspace/front", repository_id: "front" },
    { root: "/clone/api", repository_id: "api" }
  ];
  const handoff = { id: "one", repository_id: "api" };
  assert.equal(matchingRepositories(handoff, repositories).length, 2);
  assert.equal(compatibleTarget(handoff, repositories), undefined);
  assert.equal(compatibleTarget({ repository_id: "front" }, repositories), repositories[1]);
});

test("groups compatible and unmatched handoffs", () => {
  const repository = { root: "/workspace/api", repository_id: "api" };
  const compatible = { id: "one", repository_id: "api" };
  const unmatched = { id: "two", repository_id: "other" };
  assert.deepEqual(groupHandoffs([compatible, unmatched], [repository]), {
    groups: [{ repository, handoffs: [compatible] }],
    unmatched: [unmatched]
  });
});
