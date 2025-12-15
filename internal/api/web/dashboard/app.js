// API base URL - uses the node that served this dashboard page
const API_BASE = window.location.origin;

// State
let currentStatus = null;
let refreshInterval = null;
let lastNodesRefreshAt = 0;
let nodesRefreshInFlight = false;
let lastLeaderId = null;
let lastPeerCount = null;

let confirmModalState = {
  isOpen: false,
  resolver: null,
};

function openConfirmModal({
  title = "Confirm",
  message = "",
  okText = "Confirm",
  okClass = "btn btn-danger",
} = {}) {
  const modal = document.getElementById("confirm-modal");
  const titleEl = document.getElementById("confirm-title");
  const msgEl = document.getElementById("confirm-message");
  const okBtn = document.getElementById("confirm-ok");
  const cancelBtn = document.getElementById("confirm-cancel");
  if (!modal || !titleEl || !msgEl || !okBtn || !cancelBtn) {
    // If modal markup is missing, fall back to a safe "no"
    return Promise.resolve(false);
  }

  if (confirmModalState.isOpen) {
    // Only allow one modal at a time.
    return Promise.resolve(false);
  }
  confirmModalState.isOpen = true;

  titleEl.textContent = title;
  msgEl.textContent = message;
  okBtn.textContent = okText;
  okBtn.className = okClass;

  const close = (result) => {
    if (!confirmModalState.isOpen) return;
    confirmModalState.isOpen = false;
    modal.classList.add("hidden");
    modal.setAttribute("aria-hidden", "true");
    document.removeEventListener("keydown", onKeyDown);
    modal.removeEventListener("click", onBackdropClick);
    okBtn.removeEventListener("click", onOk);
    cancelBtn.removeEventListener("click", onCancel);
    const resolve = confirmModalState.resolver;
    confirmModalState.resolver = null;
    if (resolve) resolve(result);
  };

  const onOk = () => close(true);
  const onCancel = () => close(false);
  const onKeyDown = (e) => {
    if (e.key === "Escape") close(false);
  };
  const onBackdropClick = (e) => {
    if (e.target && e.target.hasAttribute("data-modal-cancel")) close(false);
  };

  okBtn.addEventListener("click", onOk);
  cancelBtn.addEventListener("click", onCancel);
  modal.addEventListener("click", onBackdropClick);
  document.addEventListener("keydown", onKeyDown);

  modal.classList.remove("hidden");
  modal.setAttribute("aria-hidden", "false");
  okBtn.focus();

  return new Promise((resolve) => {
    confirmModalState.resolver = resolve;
  });
}

// Initialize
document.addEventListener("DOMContentLoaded", () => {
  setupEventListeners();
  updateConnectionStatus("connecting");
  loadStatus();

  // Auto-refresh every 1 second for more responsive updates
  refreshInterval = setInterval(() => {
    loadStatus();
  }, 1000);
});

function setupEventListeners() {
  // Refresh button
  const refreshBtn = document.getElementById("refresh-btn");
  if (refreshBtn) {
    refreshBtn.addEventListener("click", () => {
      loadStatus();
    });
  }

  // KV operations
  const putBtn = document.getElementById("put-btn");
  if (putBtn) putBtn.addEventListener("click", handlePut);
  const getBtn = document.getElementById("get-btn");
  if (getBtn) getBtn.addEventListener("click", handleGet);
  const delBtn = document.getElementById("del-btn");
  if (delBtn) delBtn.addEventListener("click", handleDelete);

  // Cluster management
  const addNodeBtn = document.getElementById("add-node-btn");
  if (addNodeBtn) addNodeBtn.addEventListener("click", handleAddNode);
  const removeNodeBtn = document.getElementById("remove-node-btn");
  if (removeNodeBtn) removeNodeBtn.addEventListener("click", handleRemoveNode);
  const transferBtn = document.getElementById("transfer-btn");
  if (transferBtn)
    transferBtn.addEventListener("click", handleTransferLeadership);

  // Allow Enter key to submit forms
  const putKeyEl = document.getElementById("put-key");
  if (putKeyEl) {
    putKeyEl.addEventListener("keypress", (e) => {
      if (e.key === "Enter") handlePut();
    });
  }
  const putValEl = document.getElementById("put-value");
  if (putValEl) {
    putValEl.addEventListener("keypress", (e) => {
      if (e.key === "Enter") handlePut();
    });
  }
  const getKeyEl = document.getElementById("get-key");
  if (getKeyEl) {
    getKeyEl.addEventListener("keypress", (e) => {
      if (e.key === "Enter") handleGet();
    });
  }
  const delKeyEl = document.getElementById("del-key");
  if (delKeyEl) {
    delKeyEl.addEventListener("keypress", (e) => {
      if (e.key === "Enter") handleDelete();
    });
  }
}

async function loadStatus() {
  try {
    const response = await fetch(`${API_BASE}/api/status?t=${Date.now()}`, {
      cache: "no-store",
    });
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }
    const status = await response.json();
    currentStatus = status;
    updateUI(status);
    updateNodeSelector(status);
    updateConnectionStatus("connected");
  } catch (error) {
    console.error("Failed to load status:", error);
    updateConnectionStatus("disconnected");
    showToast("Failed to connect to cluster", "error");
  }
}

function httpBaseForNodeID(nodeId) {
  if (!nodeId) return null;
  // Node IDs in this repo are typically ":4000" or "localhost:4000".
  // We compute HTTP port as grpcPort + 1.
  const parts = String(nodeId).split(":");
  const portStr = parts[parts.length - 1];
  const port = parseInt(portStr, 10);
  if (!Number.isFinite(port) || port <= 0) return null;
  const httpPort = port + 1;
  return `${window.location.protocol}//${window.location.hostname}:${httpPort}`;
}

function updateNodeSelector(status) {
  const sel = document.getElementById("node-select");
  if (!sel || !status) return;

  // Populate with cluster nodes (computed http bases). We do NOT do cross-origin fetches;
  // changing selection navigates to that node's dashboard.
  const nodes = [];
  if (Array.isArray(status.peers)) {
    nodes.push(status.node_id, ...status.peers);
  } else {
    nodes.push(status.node_id);
  }

  const uniq = [];
  const seen = new Set();
  for (const n of nodes) {
    const id = String(n);
    if (!id || seen.has(id)) continue;
    seen.add(id);
    uniq.push(id);
  }

  // Rebuild options if changed.
  const currentValue = sel.value;
  sel.innerHTML = "";

  for (const id of uniq) {
    const base = httpBaseForNodeID(id);
    if (!base) continue;
    const opt = document.createElement("option");
    opt.value = base;
    // Keep labels simple: show only the node id (no localhost/host noise).
    opt.textContent = id;
    sel.appendChild(opt);
  }

  // Default selection is the current origin.
  const origin = window.location.origin;
  sel.value = uniq.length ? origin : "";
  if (
    currentValue &&
    Array.from(sel.options).some((o) => o.value === currentValue)
  ) {
    sel.value = currentValue;
  }

  if (!sel.dataset.bound) {
    sel.addEventListener("change", () => {
      const base = sel.value;
      if (!base) return;
      if (base === window.location.origin) return;
      window.location.href = `${base}/dashboard/`;
    });
    sel.dataset.bound = "1";
  }
}

async function loadClusterStatus() {
  try {
    const response = await fetch(
      `${API_BASE}/api/cluster/status?t=${Date.now()}`,
      {
        cache: "no-store",
      }
    );
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }
    return await response.json();
  } catch (error) {
    console.error("Failed to load cluster status:", error);
    return null;
  }
}

async function loadNodes() {
  try {
    const response = await fetch(
      `${API_BASE}/api/cluster/nodes?t=${Date.now()}`,
      {
        cache: "no-store",
      }
    );
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }
    const data = await response.json();
    return data.nodes || [];
  } catch (error) {
    console.error("Failed to load nodes:", error);
    return [];
  }
}

function updateUI(status) {
  // Update cluster stats - force update even if values appear the same
  const leaderId = status.leader_id || "-";
  const term = status.term || 0;
  const nodeCount = (status.peer_count || 0) + 1;
  const commitIndex = status.commit_index || 0;
  const isLeader = !!status.is_leader;

  // Force DOM update by setting innerHTML/textContent
  const leaderEl = document.getElementById("leader-id");
  if (leaderEl) leaderEl.textContent = leaderId;

  const headerLeaderEl = document.getElementById("header-leader-id");
  if (headerLeaderEl) headerLeaderEl.textContent = leaderId;

  const headerNodeEl = document.getElementById("header-node-id");
  if (headerNodeEl) headerNodeEl.textContent = status.node_id || "-";

  const termEl = document.getElementById("current-term");
  if (termEl) termEl.textContent = String(term);

  const nodeCountEl = document.getElementById("node-count");
  if (nodeCountEl) nodeCountEl.textContent = String(nodeCount);

  const commitEl = document.getElementById("commit-index");
  if (commitEl) commitEl.textContent = String(commitIndex);

  // Enable/disable cluster actions based on leader availability.
  // Server will forward mutating requests to the current leader.
  setClusterActionsEnabled(status);

  // Update nodes list (throttled). This prevents hammering /api/cluster/nodes every second,
  // which causes timeouts + reachability flicker when running many local nodes.
  const now = Date.now();
  const leaderChanged = lastLeaderId !== status.leader_id;
  const peerCountChanged = lastPeerCount !== status.peer_count;
  if (
    !nodesRefreshInFlight &&
    (leaderChanged || peerCountChanged || now - lastNodesRefreshAt > 3000)
  ) {
    nodesRefreshInFlight = true;
    lastNodesRefreshAt = now;
    lastLeaderId = status.leader_id;
    lastPeerCount = status.peer_count;
    updateNodesList(status).finally(() => {
      nodesRefreshInFlight = false;
    });
  }
}

function setClusterActionsEnabled(status) {
  const addBtn = document.getElementById("add-node-btn");
  const removeBtn = document.getElementById("remove-node-btn");
  const transferBtn = document.getElementById("transfer-btn");
  const note = document.getElementById("nodes-note");

  const hasLeader = Boolean(status && status.leader_id);
  const isLeader = Boolean(status && status.is_leader);

  // With server-side proxying, cluster changes can be initiated from any node.
  // If there's no leader elected yet, disable.
  const disable = !hasLeader;
  const message = !hasLeader
    ? "No leader elected yet. Cluster changes are temporarily unavailable."
    : isLeader
    ? "This node is leader. You can add/remove followers (cannot remove last peer)."
    : `This node is follower. Requests will be forwarded to leader ${status.leader_id}.`;

  [addBtn, removeBtn, transferBtn].forEach((btn) => {
    if (btn) {
      btn.disabled = disable;
      btn.classList.toggle("btn-disabled", disable);
    }
  });

  if (note) {
    note.textContent = message;
  }
}

async function updateNodesList(status) {
  const nodesList = document.getElementById("nodes-list");
  const nodes = await loadNodes();
  const hasLeader = Boolean(status && status.leader_id);

  // If nodes API doesn't work, use status data
  if (nodes.length === 0 && status) {
    const currentIsLeader = status.is_leader;
    nodesList.innerHTML = `
            <div class="node-item ${currentIsLeader ? "leader" : ""}">
                <div class="node-info">
                    <div class="node-id">${status.node_id} ${
      currentIsLeader ? '<span class="leader-star">★</span>' : ""
    }</div>
                    <div class="node-state">State: ${status.state}</div>
                </div>
                <div class="node-actions">
                    <span class="node-badge ${status.state}">${
      currentIsLeader ? "Leader" : status.state
    }</span>
                    ${
                      currentIsLeader
                        ? ""
                        : `<button class="btn btn-danger btn-small" onclick="handleRemoveNodeFromList('${status.node_id}')" disabled>Delete</button>`
                    }
                </div>
            </div>
        `;
    if (status.peers && status.peers.length > 0) {
      status.peers.forEach((peerId) => {
        const nodeEl = document.createElement("div");
        nodeEl.className = "node-item";
        const isPeerLeader = status.leader_id === peerId;
        nodeEl.innerHTML = `
                    <div class="node-info">
                        <div class="node-id">${peerId} ${
          isPeerLeader ? '<span class="leader-star">★</span>' : ""
        }</div>
                        <div class="node-state">State: unknown</div>
                    </div>
                    <div class="node-actions">
                        <span class="node-badge ${
                          isPeerLeader ? "leader" : "follower"
                        }">${isPeerLeader ? "Leader" : "Follower"}</span>
                        ${
                          hasLeader && !isPeerLeader
                            ? `<button class="btn btn-danger btn-small" onclick="handleRemoveNodeFromList('${peerId}')">Delete</button>`
                            : '<button class="btn btn-danger btn-small" disabled>Delete</button>'
                        }
                    </div>
                `;
        nodesList.appendChild(nodeEl);
      });
    }
  } else {
    nodesList.innerHTML = nodes
      .map((node) => {
        // Single source of truth for leader: /api/status leader_id.
        // Do NOT also trust peer-reported is_leader here; a removed node can self-elect in isolation
        // and briefly show up as a "second leader" in the UI.
        const isNodeLeader = node.id === status.leader_id;
        const isSelf = node.id === status.node_id;
        const nodeCount = (status.peer_count || 0) + 1;
        // Allow deleting the leader only if removing it won't drop us into a 1-node cluster.
        // (i.e., keep at least 2 nodes remaining; avoids "quorum confusion" in tiny clusters.)
        const canDeleteLeader = nodeCount >= 3;
        const canDelete = hasLeader && (!isNodeLeader || canDeleteLeader);
        const reachable = node.reachable !== false;
        const stateLabel = reachable ? node.state : "down";
        const badgeClass = reachable ? node.state : "down";
        const canMakeLeader = hasLeader && reachable && !isNodeLeader;
        const termText =
          typeof node.term === "number" && node.term > 0
            ? `term ${node.term}`
            : "term -";
        return `
                <div class="node-item ${isNodeLeader ? "leader" : ""} ${
          isSelf ? "self" : ""
        }">
                <div class="node-info">
                        <div class="node-id">${node.id} ${
          isNodeLeader ? '<span class="leader-star">★</span>' : ""
        } ${isSelf ? '<span class="self-tag">Current</span>' : ""}</div>
                        <div class="node-meta">
                          <span>${termText}</span>
                          <span>${
                            reachable ? "reachable" : "unreachable"
                          }</span>
                        </div>
                    </div>
                    <div class="node-actions">
                        <span class="badge ${badgeClass}">${
          isNodeLeader ? "leader" : stateLabel
        }</span>
                        ${
                          canMakeLeader
                            ? `<button class="btn btn-warning btn-small" onclick="handleMakeLeader('${node.id}')">Make leader</button>`
                            : '<button class="btn btn-warning btn-small" disabled>Make leader</button>'
                        }
                        ${
                          canDelete
                            ? `<button class="btn btn-danger btn-small" onclick="handleRemoveNodeFromList('${node.id}')">Delete</button>`
                            : '<button class="btn btn-danger btn-small" disabled>Delete</button>'
                        }
                    </div>
                </div>
            `;
      })
      .join("");
  }
}

async function handleMakeLeader(nodeId) {
  const ok = await openConfirmModal({
    title: "Make leader",
    message: `Transfer leadership to "${nodeId}"?`,
    okText: "Make leader",
    okClass: "btn btn-warning",
  });
  if (!ok) return;

  try {
    const response = await fetch(
      `${API_BASE}/api/cluster/transfer-leadership`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ target_node_id: nodeId }),
      }
    );

    const data = await response.json();
    if (!response.ok || !data.success) {
      throw new Error(data.error || "Transfer leadership failed");
    }

    showToast(`Leadership transfer initiated to: ${nodeId}`, "info");
    setTimeout(loadStatus, 800);
  } catch (error) {
    showToast(`Make leader failed: ${error.message}`, "error");
  }
}

async function handleRemoveNodeFromList(nodeId) {
  const ok = await openConfirmModal({
    title: "Remove node",
    message: `Remove node "${nodeId}" from the cluster?`,
    okText: "Remove",
    okClass: "btn btn-danger",
  });
  if (!ok) return;

  try {
    // Special-case: removing the current leader is safest if we transfer leadership first.
    // This avoids cases where the config-change can't be committed because the leader steps down.
    if (
      currentStatus &&
      nodeId === currentStatus.leader_id &&
      Array.isArray(currentStatus.peers) &&
      currentStatus.peers.length >= 2
    ) {
      const target = currentStatus.peers.find((p) => p && p !== nodeId);
      if (target) {
        showToast(`Transferring leadership to ${target}...`, "info");
        const tr = await fetch(`${API_BASE}/api/cluster/transfer-leadership`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ target_node_id: target }),
        });
        let trData = null;
        try {
          trData = await tr.json();
        } catch (_) {
          trData = null;
        }
        if (!tr.ok || !trData?.success) {
          throw new Error(trData?.error || "Transfer leadership failed");
        }

        // Wait for leader to change
        const deadline = Date.now() + 8000;
        while (Date.now() < deadline) {
          await new Promise((r) => setTimeout(r, 400));
          await loadStatus();
          if (
            currentStatus &&
            currentStatus.leader_id &&
            currentStatus.leader_id !== nodeId
          ) {
            break;
          }
        }
      }
    }

    const currentNodes = await loadNodes();
    const exists = currentNodes.some((n) => n.id === nodeId);
    if (!exists) {
      showToast(`Node ${nodeId} does not exist`, "error");
      return;
    }

    const response = await fetch(`${API_BASE}/api/cluster/remove-node`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ node_id: nodeId }),
    });

    const data = await response.json();

    if (!response.ok || !data.success) {
      throw new Error(data.error || "Remove node failed");
    }

    showToast(`Successfully removed node: ${nodeId}`, "success");
    loadStatus();
  } catch (error) {
    showToast(`Remove node failed: ${error.message}`, "error");
  }
}

function updateConnectionStatus(status) {
  const indicator = document.getElementById("connection-status");
  if (!indicator) return;

  const cls =
    status === "connected"
      ? "pill pill-ok"
      : status === "connecting"
      ? "pill pill-warn"
      : "pill pill-bad";
  indicator.className = cls;
  indicator.textContent =
    status === "connected"
      ? "Connected"
      : status === "connecting"
      ? "Connecting..."
      : "Disconnected";
}

async function handlePut() {
  const key = document.getElementById("put-key").value.trim();
  const value = document.getElementById("put-value").value.trim();

  if (!key) {
    showToast("Key is required", "error");
    return;
  }

  try {
    const response = await fetch(
      `${API_BASE}/api/kv/${encodeURIComponent(key)}`,
      {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ value }),
      }
    );

    const data = await response.json();

    if (!response.ok) {
      throw new Error(data.error || "Put failed");
    }

    showToast(`Successfully put ${key} = ${value}`, "success");
    // Reset fields
    document.getElementById("put-key").value = "";
    document.getElementById("put-value").value = "";
    loadStatus();
  } catch (error) {
    showToast(`Put failed: ${error.message}`, "error");
  }
}

async function handleGet() {
  const key = document.getElementById("get-key").value.trim();
  const allowStale = document.getElementById("allow-stale").checked;

  if (!key) {
    showToast("Key is required", "error");
    return;
  }

  try {
    const url = `${API_BASE}/api/kv/${encodeURIComponent(key)}${
      allowStale ? "?allow_stale=true" : ""
    }`;
    const response = await fetch(url);
    const data = await response.json();

    const resultBox = document.getElementById("get-result");
    if (!response.ok) {
      resultBox.textContent = `Error: ${data.error || "Get failed"}`;
      resultBox.style.color = "#fb7185";
      return;
    }

    if (data.found) {
      resultBox.textContent = data.value || "(empty)";
      resultBox.style.color = "rgba(255, 255, 255, 0.86)";
      showToast(`Found key: ${key}`, "success");
    } else {
      resultBox.textContent = "(not found)";
      resultBox.style.color = "rgba(255, 255, 255, 0.55)";
    }
    // Reset field after get
    document.getElementById("get-key").value = "";
  } catch (error) {
    const resultBox = document.getElementById("get-result");
    resultBox.textContent = `Error: ${error.message}`;
    resultBox.style.color = "#fb7185";
    showToast(`Get failed: ${error.message}`, "error");
  }
}

async function handleDelete() {
  const key = document.getElementById("del-key").value.trim();

  if (!key) {
    showToast("Key is required", "error");
    return;
  }

  const ok = await openConfirmModal({
    title: "Delete key",
    message: `Delete key "${key}"?`,
    okText: "Delete",
    okClass: "btn btn-danger",
  });
  if (!ok) return;

  try {
    const response = await fetch(
      `${API_BASE}/api/kv/${encodeURIComponent(key)}`,
      {
        method: "DELETE",
      }
    );

    const data = await response.json();

    if (!response.ok) {
      throw new Error(data.error || "Delete failed");
    }

    showToast(`Successfully deleted ${key}`, "success");
    // Reset field after delete
    document.getElementById("del-key").value = "";
    loadStatus();
  } catch (error) {
    showToast(`Delete failed: ${error.message}`, "error");
  }
}

async function handleAddNode() {
  const ctrl = new AbortController();
  const timeout = setTimeout(() => ctrl.abort(), 7000);

  try {
    const response = await fetch(`${API_BASE}/api/cluster/spawn-node`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      signal: ctrl.signal,
    });

    let data = null;
    try {
      data = await response.json();
    } catch (_) {
      data = null;
    }

    if (!response.ok || !data?.success) {
      const leaderHttp = data?.leader_http;
      if (leaderHttp) {
        showToast(
          `Not leader. Redirecting to leader dashboard: ${leaderHttp}`,
          "warning"
        );
        setTimeout(() => {
          window.location.href = `${leaderHttp}/dashboard/`;
        }, 900);
        return;
      }
      throw new Error(data?.error || "Add node failed");
    }

    showToast(`Successfully added node: ${data.node_id}`, "success");
    loadStatus();
  } catch (error) {
    const msg =
      error?.name === "AbortError"
        ? "request timed out (cluster may be busy)"
        : error?.message || String(error);
    showToast(`Add node failed: ${msg}`, "error");
  } finally {
    clearTimeout(timeout);
  }
}

async function handleRemoveNode() {
  const nodeId = document.getElementById("remove-node-id").value.trim();

  if (!nodeId) {
    showToast("Node ID is required", "error");
    return;
  }

  const ok = await openConfirmModal({
    title: "Remove node",
    message: `Remove node "${nodeId}" from the cluster?`,
    okText: "Remove",
    okClass: "btn btn-danger",
  });
  if (!ok) return;

  try {
    const currentNodes = await loadNodes();
    const exists = currentNodes.some((n) => n.id === nodeId);
    if (!exists) {
      showToast(`Node ${nodeId} does not exist`, "error");
      return;
    }

    const response = await fetch(`${API_BASE}/api/cluster/remove-node`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ node_id: nodeId }),
    });

    const data = await response.json();

    if (!response.ok || !data.success) {
      throw new Error(data.error || "Remove node failed");
    }

    showToast(`Successfully removed node: ${nodeId}`, "success");
    // Reset field after removing
    document.getElementById("remove-node-id").value = "";
    loadStatus();
  } catch (error) {
    showToast(`Remove node failed: ${error.message}`, "error");
  }
}

async function handleTransferLeadership() {
  const targetNodeId = document.getElementById("transfer-target").value.trim();

  if (!targetNodeId) {
    showToast("Target Node ID is required", "error");
    return;
  }

  const ok = await openConfirmModal({
    title: "Transfer leadership",
    message: `Transfer leadership to "${targetNodeId}"?`,
    okText: "Transfer",
    okClass: "btn btn-warning",
  });
  if (!ok) return;

  try {
    const response = await fetch(
      `${API_BASE}/api/cluster/transfer-leadership`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ target_node_id: targetNodeId }),
      }
    );

    const data = await response.json();

    if (!response.ok || !data.success) {
      throw new Error(data.error || "Transfer leadership failed");
    }

    showToast(`Leadership transfer initiated to: ${targetNodeId}`, "info");
    // Reset field after transfer
    document.getElementById("transfer-target").value = "";

    // Wait a bit and refresh status
    setTimeout(loadStatus, 1000);
  } catch (error) {
    showToast(`Transfer leadership failed: ${error.message}`, "error");
  }
}

function showToast(message, type = "info") {
  const container = document.getElementById("toast-container");
  const toast = document.createElement("div");
  toast.className = `toast ${type}`;
  toast.textContent = message;

  container.appendChild(toast);

  // Remove after 3 seconds
  setTimeout(() => {
    toast.style.animation = "slideIn 0.3s ease-out reverse";
    setTimeout(() => {
      container.removeChild(toast);
    }, 300);
  }, 3000);
}
