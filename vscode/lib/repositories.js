"use strict";

function repositoryKey(repository) {
  return String(repository.root || repository.rootUri?.fsPath || "").toLowerCase();
}

function matchingRepositories(handoff, repositories) {
  if (!handoff?.repository_id) return [];
  return (repositories || []).filter((repository) => repository.repository_id === handoff.repository_id);
}

function groupHandoffs(handoffs, repositories) {
  const groups = (repositories || []).map((repository) => ({ repository, handoffs: [] }));
  const unmatched = [];
  for (const handoff of handoffs || []) {
    const matches = matchingRepositories(handoff, repositories);
    if (matches.length === 0) unmatched.push(handoff);
    else for (const match of matches) groups.find((group) => group.repository === match).handoffs.push(handoff);
  }
  return { groups: groups.filter((group) => group.handoffs.length), unmatched };
}

function compatibleTarget(handoff, repositories) {
  const matches = matchingRepositories(handoff, repositories);
  return matches.length === 1 ? matches[0] : undefined;
}

module.exports = { compatibleTarget, groupHandoffs, matchingRepositories, repositoryKey };
