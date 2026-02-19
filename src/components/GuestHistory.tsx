import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ScrollArea } from "@/components/ui/scroll-area";

interface HistoryEntry {
  date: string;
  type: "incident" | "improvement";
  description: string;
  scoreChange: number;
}

interface GuestHistoryProps {
  history: HistoryEntry[];
}

export const GuestHistory = ({ history }: GuestHistoryProps) => {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Guest History</CardTitle>
      </CardHeader>
      <CardContent>
        <ScrollArea className="h-[200px] pr-4">
          {history.map((entry, index) => (
            <div
              key={index}
              className="mb-4 flex items-center justify-between border-b border-border pb-2 last:border-0"
            >
              <div>
                <p className="text-sm font-medium">{entry.description}</p>
                <p className="text-xs text-muted-foreground">{entry.date}</p>
              </div>
              <span
                className={`text-sm font-bold ${
                  entry.scoreChange >= 0 ? "text-green-500" : "text-red-500"
                }`}
              >
                {entry.scoreChange >= 0 ? "+" : ""}
                {entry.scoreChange}
              </span>
            </div>
          ))}
        </ScrollArea>
      </CardContent>
    </Card>
  );
};