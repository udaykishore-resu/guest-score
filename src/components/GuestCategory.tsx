import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";

interface GuestCategoryProps {
  score: number;
}

export const GuestCategory = ({ score }: GuestCategoryProps) => {
  const getCategory = (score: number) => {
    if (score >= 90) return { name: "VIP", color: "bg-purple-500" };
    if (score >= 70) return { name: "Premium", color: "bg-green-500" };
    if (score >= 50) return { name: "Regular", color: "bg-blue-500" };
    return { name: "Watch List", color: "bg-red-500" };
  };

  const category = getCategory(score);

  return (
    <Card>
      <CardHeader>
        <CardTitle>Guest Category</CardTitle>
      </CardHeader>
      <CardContent>
        <div className="flex items-center justify-between">
          <span className="text-lg font-medium text-muted-foreground">
            Current Status
          </span>
          <Badge className={category.color}>{category.name}</Badge>
        </div>
      </CardContent>
    </Card>
  );
};