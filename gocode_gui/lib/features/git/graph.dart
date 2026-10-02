// Ported verbatim from goide (app/lib/git/graph.dart).

/// Commit-graph lane layout for the history view.
///
/// Commits arrive in topological order (children before parents). Each lane
/// holds the hash of the commit expected next on it. For every row we record
/// the lanes entering from above and leaving below, so a painter can draw
/// the node, straight pass-through lines, merges into the node and branches
/// out of it.
library;

class GraphRow {
  const GraphRow({
    required this.column,
    required this.lanesIn,
    required this.lanesOut,
    required this.parentLanes,
  });

  /// Lane of this commit's node.
  final int column;

  /// Lanes entering the row from the top (hash expected per lane, or null).
  /// A lane whose hash moves to another index in [lanesOut] bends there.
  final List<String?> lanesIn;

  /// Lanes leaving the row at the bottom.
  final List<String?> lanesOut;

  /// Bottom lanes the node connects to (its parents).
  final List<int> parentLanes;

  int get width {
    var w = column + 1;
    for (var i = 0; i < lanesIn.length; i++) {
      if (lanesIn[i] != null && i + 1 > w) w = i + 1;
    }
    for (var i = 0; i < lanesOut.length; i++) {
      if (lanesOut[i] != null && i + 1 > w) w = i + 1;
    }
    return w;
  }
}

/// Computes graph rows for commits given as (hash, parents) in topo order.
List<GraphRow> layoutGraph(List<(String hash, List<String> parents)> commits) {
  final lanes = <String?>[];
  final rows = <GraphRow>[];

  int freeLane() {
    final i = lanes.indexOf(null);
    if (i >= 0) return i;
    lanes.add(null);
    return lanes.length - 1;
  }

  for (final (hash, parents) in commits) {
    var col = lanes.indexOf(hash);
    if (col < 0) col = freeLane(); // branch tip: starts a new lane
    // Snapshot before placing the node, so a tip has no line from above.
    final lanesIn = List<String?>.of(lanes);
    lanes[col] = hash;
    // Other lanes waiting for this commit merge into the node.
    for (var i = 0; i < lanes.length; i++) {
      if (i != col && lanes[i] == hash) lanes[i] = null;
    }

    final parentLanes = <int>[];
    if (parents.isEmpty) {
      lanes[col] = null; // root commit: lane ends
    } else {
      // First parent continues this lane. If another lane already expects
      // it, the two converge onto the leftmost of them (the other lane
      // bends into it), keeping long-lived lines on the left.
      final existing = lanes.indexOf(parents.first);
      if (existing >= 0 && existing < col) {
        lanes[col] = null;
        parentLanes.add(existing);
      } else if (existing > col) {
        lanes[existing] = null;
        lanes[col] = parents.first;
        parentLanes.add(col);
      } else {
        lanes[col] = parents.first;
        parentLanes.add(col);
      }
      for (final p in parents.skip(1)) {
        var lane = lanes.indexOf(p);
        if (lane < 0) {
          lane = freeLane();
          lanes[lane] = p;
        }
        parentLanes.add(lane);
      }
    }
    while (lanes.isNotEmpty && lanes.last == null) {
      lanes.removeLast();
    }
    rows.add(
      GraphRow(
        column: col,
        lanesIn: lanesIn,
        lanesOut: List<String?>.of(lanes),
        parentLanes: parentLanes,
      ),
    );
  }
  return rows;
}
