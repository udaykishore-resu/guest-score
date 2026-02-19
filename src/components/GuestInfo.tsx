import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";

interface GuestInfoProps {
  guest: {
    name: string;
    email: string;
    status: "active" | "banned" | "flagged";
    lastStay: string;
    totalStays: number;
  };
}

export const GuestInfo = ({ guest }: GuestInfoProps) => {
  const statusColors = {
    active: "bg-green-500",
    banned: "bg-red-500",
    flagged: "bg-yellow-500",
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center justify-between">
          Guest Information
          <Badge className={statusColors[guest.status]}>
            {guest.status.charAt(0).toUpperCase() + guest.status.slice(1)}
          </Badge>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <div>
          <p className="text-sm font-medium text-muted-foreground">Name</p>
          <p className="text-lg">{guest.name}</p>
        </div>
        <div>
          <p className="text-sm font-medium text-muted-foreground">Email</p>
          <p className="text-lg">{guest.email}</p>
        </div>
        <div className="grid grid-cols-2 gap-4">
          <div>
            <p className="text-sm font-medium text-muted-foreground">Last Stay</p>
            <p className="text-lg">{guest.lastStay}</p>
          </div>
          <div>
            <p className="text-sm font-medium text-muted-foreground">Total Stays</p>
            <p className="text-lg">{guest.totalStays}</p>
          </div>
        </div>
      </CardContent>
    </Card>
  );
};